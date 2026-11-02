import { useEffect, useState } from "react";
import type { Coverage, Journalist, JournalistImportResult, JournalistInput, NewsArticle, NewsSearch, OutletTier, SmartListRule } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, Empty, ErrorText, field, FilePicker, Loading, Menu, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// News watch: saved searches that run on a schedule through the
// workspace's own web search connection (Geonode), the articles they find,
// and coverage: articles about a brand, each on its outlet's company
// record, to confirm or dismiss; and journalists: the articles' authors
// whose address was found and the ones people added, as person records
// with beats and outlet tiers, and media lists of them (smart lists with
// news search, beat and tier rules). The first part of digital PR.

type Props = { ws: string; canManage: boolean; canLists: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "coverage" | "articles" | "journalists" | "searches";
type CoverageStatus = Coverage["status"];

const statusLabel: Record<CoverageStatus, string> = { new: "New", confirmed: "Confirmed", dismissed: "Dismissed" };
const statusTone: Record<CoverageStatus, Tone> = { new: "blue", confirmed: "green", dismissed: "grey" };
const errorLabel: Record<string, string> = {
  connector_needed: "needs a web search connection",
  usage_limit_reached: "the connection reached its usage limit",
  connector_quota: "Geonode was busy; it tries again",
  connector_unavailable: "Geonode didn't answer; it tries again",
  connector_auth: "Geonode refused the key",
};
const day = (t: string) => new Date(t).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
const path = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

export function News({ ws, canManage, canLists, go }: Props) {
  const [section, setSection] = useState<Section>("coverage");
  const brandKey = useBrand(ws).query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};
  return (
    <div className="space-y-6">
      <PageHeader
        title="News watch"
        description="Saved news searches that run on a schedule through your own Geonode account: follow a topic and who writes about it, and keep every article about your brand as its coverage. Agents use list_coverage and create_news_search."
      />
      <Tabs value={section} onValueChange={(v) => setSection(v as Section)} className="max-w-full overflow-x-auto">
        <TabsList className="h-8">
          <TabsTrigger value="coverage" className="px-3 text-[12px]">Coverage</TabsTrigger>
          <TabsTrigger value="articles" className="px-3 text-[12px]">Articles</TabsTrigger>
          <TabsTrigger value="journalists" className="px-3 text-[12px]">Journalists</TabsTrigger>
          <TabsTrigger value="searches" className="px-3 text-[12px]">Searches</TabsTrigger>
        </TabsList>
      </Tabs>
      {section === "coverage" && <CoverageList ws={ws} canManage={canManage} brand={brand} />}
      {section === "articles" && <Articles ws={ws} brand={brand} />}
      {section === "journalists" && <Journalists ws={ws} canManage={canManage} canLists={canLists} brand={brand} />}
      {section === "searches" && <Searches ws={ws} canManage={canManage} brand={brand} go={go} />}
    </div>
  );
}

function CoverageList({ ws, canManage, brand }: { ws: string; canManage: boolean; brand: { brand?: string } }) {
  const { active, byId } = useBrand(ws);
  const [show, setShow] = useState<"open" | "dismissed">("open");
  const [list, setList] = useState<Coverage[] | null>(null);
  const [url, setURL] = useState("");
  const [brandID, setBrandID] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const forBrand = brand.brand ? active.find((b) => b.handle === brand.brand)?.id : brandID || active[0]?.id;

  useEffect(() => {
    setList(null);
    api.GET("/v1/workspaces/{ws}/coverage", { params: { path: { ws }, query: { ...brand, status: show === "open" ? "new,confirmed" : "dismissed", limit: 200 } } })
      .then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setList(data.data);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, show, reload, brand.brand]);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!url.trim() || !forBrand) return;
    setBusy("add");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/coverage", { params: { path: { ws } }, body: { brand_id: forBrand, url: url.trim() } });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setURL("");
    toast(`Added ${data.domain}.`);
    setReload((n) => n + 1);
  };
  const move = async (c: Coverage, body: { status?: CoverageStatus; linked?: boolean }) => {
    setBusy(c.id);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/coverage/{coverage}", { params: { path: { ws, coverage: c.id } }, body });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const actions = (c: Coverage) =>
    canManage && (
      <div className="flex items-center justify-end gap-1">
        {c.status === "new" && <Button variant="ghost" disabled={busy === c.id} onClick={() => move(c, { status: "confirmed" })}>Confirm</Button>}
        <Menu
          aria={`More for ${c.url}`}
          items={[
            c.status !== "dismissed" && { label: "Dismiss: not about us", onClick: () => move(c, { status: "dismissed" }) },
            c.status !== "new" && { label: "Back to new", onClick: () => move(c, { status: "new" }) },
            c.linked !== true && { label: "It links to us", onClick: () => move(c, { linked: true }) },
            c.linked !== false && { label: "It doesn't link to us", onClick: () => move(c, { linked: false }) },
          ]}
        />
      </div>
    );
  const linked = (c: Coverage) => (c.linked === null ? "not checked" : c.linked ? "links to you" : "no link");
  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      {canManage && active.length > 0 && (
        <form onSubmit={add} className="flex flex-wrap items-center gap-2">
          <input className={`${field} w-80 max-w-full`} aria-label="Article" placeholder="https://news.example.org/2026/10/story" value={url} onChange={(e) => setURL(e.target.value)} maxLength={2048} />
          {!brand.brand && active.length > 1 && (
            <select className={field} aria-label="Brand" value={forBrand} onChange={(e) => setBrandID(e.target.value)}>
              {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
          )}
          <Button type="submit" disabled={!url.trim() || busy === "add"}>Add an article</Button>
        </form>
      )}
      {!list ? (
        <Loading />
      ) : (
        <DataTable
          data={list}
          rowId={(c) => c.id}
          searchPlaceholder="Search coverage"
          toolbar={
            <select className={field} aria-label="Show" value={show} onChange={(e) => setShow(e.target.value as "open" | "dismissed")}>
              <option value="open">New and confirmed</option>
              <option value="dismissed">Dismissed</option>
            </select>
          }
          empty={show === "open" ? "No coverage yet. Save a coverage search for your brand under Searches, or add an article above." : "Nothing dismissed."}
          mobile={(c) => (
            <div className="space-y-1">
              <div className="flex items-start justify-between gap-2">
                <span className="font-medium break-all">{c.domain}</span>
                <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge>
              </div>
              <div className="text-[13px]">{c.title || path(c.url)}</div>
              <div className="text-xs text-muted-foreground">{[byId(c.brand_id)?.name, linked(c), `found ${ago(c.found_at)}`].filter(Boolean).join(" · ")}</div>
              {actions(c)}
            </div>
          )}
          columns={[
            {
              id: "article",
              header: "Article",
              accessorFn: (c) => `${c.title} ${c.url}`,
              cell: ({ row: { original: c } }) => (
                <div className="min-w-0 space-y-0.5">
                  <a className="block max-w-96 truncate font-medium underline-offset-2 hover:underline" href={c.url} target="_blank" rel="noreferrer noopener nofollow" title={c.title || c.url}>{c.title || path(c.url)}</a>
                  {c.snippet && <div className="max-w-96 truncate text-[12px] text-muted-foreground" title={c.snippet}>{c.snippet}</div>}
                </div>
              ),
            },
            { id: "outlet", header: "Outlet", accessorFn: (c) => c.domain, cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">{row.original.domain}</span> },
            { id: "brand", header: "Brand", accessorFn: (c) => byId(c.brand_id)?.name ?? "", cell: ({ row }) => <span className="text-[12px]">{byId(row.original.brand_id)?.name ?? ""}</span> },
            { id: "linked", header: "Link", accessorFn: (c) => linked(c), cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">{linked(row.original)}</span> },
            {
              id: "status",
              header: "Status",
              accessorFn: (c) => c.status,
              cell: ({ row: { original: c } }) => (
                <div className="space-y-0.5">
                  <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge>
                  <div className="text-[11px] text-muted-foreground">{c.source === "manual" ? "added by hand" : `found ${ago(c.found_at)}`}</div>
                </div>
              ),
            },
            { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row }) => actions(row.original) },
          ]}
        />
      )}
    </div>
  );
}

function Articles({ ws, brand }: { ws: string; brand: { brand?: string } }) {
  const [list, setList] = useState<NewsArticle[] | null>(null);
  const [searches, setSearches] = useState<NewsSearch[]>([]);
  const [search, setSearch] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/news-searches", { params: { path: { ws }, query: brand } }).then(({ data }) => setSearches(data?.data ?? []));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand.brand]);
  useEffect(() => {
    setList(null);
    api.GET("/v1/workspaces/{ws}/news-articles", { params: { path: { ws }, query: { ...brand, ...(search ? { search_id: search } : {}), limit: 200 } } })
      .then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setError("");
        setList(data.data);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, search, brand.brand]);
  const name = (id: string) => searches.find((s) => s.id === id)?.name ?? "";
  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <div className="space-y-2">
      {error && <ErrorText>{error}</ErrorText>}
      <DataTable
        data={list}
        rowId={(a) => a.id}
        searchPlaceholder="Search articles"
        toolbar={
          searches.length > 1 && (
            <select className={field} aria-label="Search" value={search} onChange={(e) => setSearch(e.target.value)}>
              <option value="">Every search</option>
              {searches.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          )
        }
        empty="No articles yet. They show once a saved search has run."
        mobile={(a) => (
          <div className="space-y-1">
            <span className="font-medium break-all">{a.domain}</span>
            <div className="text-[13px]">{a.title || path(a.url)}</div>
            <div className="text-xs text-muted-foreground">{a.search_ids.map(name).filter(Boolean).join(", ")} · {ago(a.first_seen_at)}</div>
          </div>
        )}
        columns={[
          {
            id: "article",
            header: "Article",
            accessorFn: (a) => `${a.title} ${a.url}`,
            cell: ({ row: { original: a } }) => (
              <div className="min-w-0 space-y-0.5">
                <a className="block max-w-96 truncate font-medium underline-offset-2 hover:underline" href={a.url} target="_blank" rel="noreferrer noopener nofollow" title={a.title || a.url}>{a.title || path(a.url)}</a>
                {(a.author || a.published_at) && (
                  <div className="text-[12px] text-muted-foreground">{[a.author && `By ${a.author}`, a.published_at && day(a.published_at)].filter(Boolean).join(" · ")}</div>
                )}
                {a.snippet && <div className="max-w-96 truncate text-[12px] text-muted-foreground" title={a.snippet}>{a.snippet}</div>}
              </div>
            ),
          },
          { id: "outlet", header: "Outlet", accessorFn: (a) => a.domain, cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">{row.original.domain}</span> },
          { id: "found", header: "Found by", accessorFn: (a) => a.search_ids.map(name).join(", "), cell: ({ row }) => <span className="text-[12px]">{row.original.search_ids.map(name).filter(Boolean).join(", ")}</span> },
          { id: "seen", header: "First seen", accessorFn: (a) => a.first_seen_at, cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">{ago(row.original.first_seen_at)}</span> },
        ]}
      />
    </div>
  );
}

const tierLabel: Record<OutletTier, string> = { national: "National", trade: "Trade", regional: "Regional", local: "Local", newsletter: "Newsletter or blog" };
const tiers = Object.keys(tierLabel) as OutletTier[];
const splitBeats = (s: string) => s.split(/[;,|]/).map((b) => b.trim()).filter(Boolean);

function Journalists({ ws, canManage, canLists, brand }: { ws: string; canManage: boolean; canLists: boolean; brand: { brand?: string } }) {
  const brandID = useBrand(ws).brand?.id;
  const [list, setList] = useState<Journalist[] | null>(null);
  const [searches, setSearches] = useState<NewsSearch[]>([]);
  const [search, setSearch] = useState("");
  const [beat, setBeat] = useState("");
  const [tier, setTier] = useState<OutletTier | "">("");
  const [beats, setBeats] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [dialog, setDialog] = useState<"add" | "import" | Journalist | null>(null);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/news-searches", { params: { path: { ws }, query: brand } }).then(({ data }) => setSearches(data?.data ?? []));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand.brand]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/journalists", {
      params: { path: { ws }, query: { ...brand, ...(search ? { search_id: search } : {}), ...(beat ? { beat } : {}), ...(tier ? { tier } : {}), limit: 200 } },
    }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setList(data.data);
      // The beats to filter by: every one seen so far.
      setBeats((have) => {
        const seen = new Map(have.map((b) => [b.toLowerCase(), b]));
        for (const j of data.data) for (const b of j.beats) if (!seen.has(b.toLowerCase())) seen.set(b.toLowerCase(), b);
        return [...seen.values()].sort((a, b) => a.localeCompare(b));
      });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, search, beat, tier, brand.brand, reload]);

  // A media list is a smart list of the journalists picked here: who wrote
  // about the search's topic in the last year, with the beat, on outlets of
  // the tier. It keeps itself up to date.
  const mediaList = async () => {
    const s = searches.find((x) => x.id === search);
    // A media list is a smart list, which belongs to one brand.
    if (!brandID) return setError("Pick a brand in the brand switcher first: a media list belongs to one brand.");
    const rules: SmartListRule[] = s ? [{ news_search: s.id, op: "exists", within_days: 365 }] : [{ attribute: "journalist", op: "eq", value: true }];
    if (beat) rules.push({ attribute: "beats", op: "contains", value: beat });
    if (tier) rules.push({ company_attribute: "outlet_tier", op: "eq", value: tier });
    const name = ["Journalists", s?.name, beat, tier && tierLabel[tier]].filter(Boolean).join(": ");
    const about = [s && `wrote an article the "${s.name}" news search found in the last year`, beat && `cover ${beat}`, tier && `write for ${tierLabel[tier].toLowerCase()} outlets`].filter(Boolean).join(", ");
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/smart-lists", {
      params: { path: { brand: brandID } },
      body: { name: name.slice(0, 200), description: `Journalists who ${about}.`.slice(0, 2000), rules },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Media list made. It's under Lists and keeps itself up to date.");
  };
  const patch = async (j: Journalist, body: { beats?: string[]; job_title?: string | null; journalist?: boolean }) => {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/journalists/{record}", { params: { path: { ws, record: j.record_id } }, body });
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const unmark = async (j: Journalist) => {
    if (!(await confirmAction(`Take ${j.name || j.email} off the list?`, "Their person record stays. The news watch won't add them back.", "Take off"))) return;
    await patch(j, { journalist: false });
  };

  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const filtered = search || beat || tier;
  return (
    <div className="space-y-2">
      {error && <ErrorText>{error}</ErrorText>}
      <p className="text-[12px] text-muted-foreground">
        The authors of the articles your searches found, once the free email finder found their address at the outlet, and the journalists you added. Each is a person record on their outlet's company.
      </p>
      <DataTable
        data={list}
        rowId={(j) => j.record_id}
        searchPlaceholder="Search journalists"
        toolbar={
          <div className="flex flex-wrap items-center gap-2">
            {searches.length > 0 && (
              <select className={field} aria-label="Topic" value={search} onChange={(e) => setSearch(e.target.value)}>
                <option value="">Every topic</option>
                {searches.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
            )}
            {(beats.length > 0 || beat) && (
              <select className={field} aria-label="Beat" value={beat} onChange={(e) => setBeat(e.target.value)}>
                <option value="">Every beat</option>
                {beats.map((b) => <option key={b} value={b}>{b}</option>)}
              </select>
            )}
            <select className={field} aria-label="Outlet tier" value={tier} onChange={(e) => setTier(e.target.value as OutletTier | "")}>
              <option value="">Every outlet tier</option>
              {tiers.map((t) => <option key={t} value={t}>{tierLabel[t]}</option>)}
            </select>
            {canLists && filtered && <Button variant="ghost" disabled={busy} onClick={mediaList}>Make a media list</Button>}
            {canManage && <Button variant="ghost" onClick={() => setDialog("import")}>Import CSV</Button>}
            {canManage && <Button onClick={() => setDialog("add")}>Add journalist</Button>}
          </div>
        }
        empty={filtered ? "No journalists match." : searches.length === 0 ? "No journalists yet. Add the ones you know, or save a search under Searches; its articles' authors show here once their addresses are found." :
          "No journalists yet. Add the ones you know; the authors of your searches' articles show here once their addresses are found."}
        mobile={(j) => (
          <div className="space-y-1">
            <div className="font-medium">{j.name || j.email}</div>
            <div className="text-xs text-muted-foreground">{[j.outlets.join(", "), `${j.articles} article${j.articles === 1 ? "" : "s"}`].join(" · ")}</div>
            {j.beats.length > 0 && <div className="text-[12px]">{j.beats.join(", ")}</div>}
            {j.latest_article && <div className="text-[12px]">{j.latest_article.title}</div>}
          </div>
        )}
        columns={[
          {
            id: "name",
            header: "Journalist",
            accessorFn: (j) => `${j.name} ${j.email ?? ""} ${j.job_title ?? ""}`,
            cell: ({ row: { original: j } }) => (
              <div className="min-w-0 space-y-0.5">
                <div className="font-medium">{j.name || j.email}</div>
                {j.job_title && <div className="text-[12px] text-muted-foreground">{j.job_title}</div>}
                {j.email && <div className="font-mono text-[12px] text-muted-foreground" title={j.confidence ? `The email finder was ${j.confidence}% sure` : undefined}>{j.email}</div>}
              </div>
            ),
          },
          {
            id: "beats",
            header: "Beats",
            accessorFn: (j) => j.beats.join(", "),
            cell: ({ row: { original: j } }) => (
              <div className="flex max-w-72 flex-wrap gap-1">
                {j.beats.map((b) => <Badge key={b} tone="blue">{b}</Badge>)}
                {canManage && j.suggested_beats.map((b) => (
                  <button key={b} type="button" className="rounded-full border border-dashed px-2 text-[11px] text-muted-foreground hover:text-foreground" title="Suggested from the searches their articles came up in: add it" onClick={() => patch(j, { beats: [...j.beats, b] })}>
                    + {b}
                  </button>
                ))}
              </div>
            ),
          },
          {
            id: "outlets",
            header: "Writes for",
            accessorFn: (j) => j.outlets.join(", "),
            cell: ({ row: { original: j } }) => (
              <div className="space-y-0.5 text-[12px]">
                {j.outlet_records.map((o) => (
                  <div key={o.record_id} className="flex flex-wrap items-center gap-1">
                    <span>{o.domain ?? o.name}</span>
                    {o.tier && <Badge tone="grey">{tierLabel[o.tier]}</Badge>}
                  </div>
                ))}
                {j.outlets.filter((d) => !j.outlet_records.some((o) => o.domain === d)).map((d) => <div key={d}>{d}</div>)}
              </div>
            ),
          },
          { id: "articles", header: "Articles", accessorFn: (j) => j.articles, meta: { align: "right", mono: true }, cell: ({ row }) => row.original.articles },
          {
            id: "latest",
            header: "Latest",
            accessorFn: (j) => j.latest_article?.published_at ?? "",
            cell: ({ row: { original: j } }) =>
              j.latest_article ? (
                <div className="min-w-0 space-y-0.5">
                  <a className="block max-w-80 truncate text-[13px] underline-offset-2 hover:underline" href={j.latest_article.url} target="_blank" rel="noreferrer noopener nofollow" title={j.latest_article.title}>{j.latest_article.title || path(j.latest_article.url)}</a>
                  {j.latest_article.published_at && <div className="text-[11px] text-muted-foreground">{day(j.latest_article.published_at)}</div>}
                </div>
              ) : <span className="text-[12px] text-muted-foreground">None found yet</span>,
          },
          { id: "topics", header: "Topics", accessorFn: (j) => j.topics.map((t) => t.name).join(", "), cell: ({ row }) => <span className="text-[12px]">{row.original.topics.map((t) => t.name).join(", ")}</span> },
          {
            id: "actions",
            header: () => <span className="sr-only">Actions</span>,
            enableSorting: false,
            meta: { align: "right" },
            cell: ({ row: { original: j } }) =>
              canManage && (
                <Menu
                  aria={`More for ${j.name || j.email}`}
                  items={[
                    { label: "Edit beats and title", onClick: () => setDialog(j) },
                    { label: "Take off the list", onClick: () => unmark(j), danger: true },
                  ]}
                />
              ),
          },
        ]}
      />
      {dialog === "add" && <AddJournalist ws={ws} onClose={() => setDialog(null)} onDone={() => { setDialog(null); setReload((n) => n + 1); }} />}
      {dialog === "import" && <ImportJournalists ws={ws} brand={brand} onClose={() => setDialog(null)} onDone={() => { setDialog(null); setReload((n) => n + 1); }} />}
      {dialog && typeof dialog === "object" && (
        <EditJournalist j={dialog} onClose={() => setDialog(null)} onSave={async (body) => { await patch(dialog, body); setDialog(null); }} />
      )}
    </div>
  );
}

function AddJournalist({ ws, onClose, onDone }: { ws: string; onClose: () => void; onDone: () => void }) {
  const { active, brand: current } = useBrand(ws);
  const [v, setV] = useState({ name: "", email: "", job_title: "", outlet_website: "", outlet_name: "", outlet_tier: "" as OutletTier | "", beats: "", brand_id: current?.id ?? active[0]?.id ?? "" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    const body: JournalistInput = {
      name: v.name.trim(),
      email: v.email.trim(),
      ...(v.job_title.trim() ? { job_title: v.job_title.trim() } : {}),
      ...(v.outlet_website.trim() ? { outlet_website: v.outlet_website.trim() } : {}),
      ...(v.outlet_name.trim() ? { outlet_name: v.outlet_name.trim() } : {}),
      ...(v.outlet_tier ? { outlet_tier: v.outlet_tier } : {}),
      ...(splitBeats(v.beats).length ? { beats: splitBeats(v.beats) } : {}),
      ...(v.brand_id ? { brand_id: v.brand_id } : {}),
    };
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journalists", { params: { path: { ws } }, body });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast(`${data.name || data.email} is on the list.`);
    onDone();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Add a journalist</DialogTitle>
            <DialogDescription>Someone you already know. If they are already a person here, they're marked as a journalist and keep their record.</DialogDescription>
          </DialogHeader>
          {error && <ErrorText>{error}</ErrorText>}
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input required autoFocus className={`${field} w-full`} maxLength={200} placeholder="Ana Diaz" value={v.name} onChange={(e) => setV({ ...v, name: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Email</span>
              <input required type="email" className={`${field} w-full`} placeholder="ana@news.example.org" value={v.email} onChange={(e) => setV({ ...v, email: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Job title</span>
              <input className={`${field} w-full`} maxLength={200} placeholder="Technology reporter" value={v.job_title} onChange={(e) => setV({ ...v, job_title: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Beats</span>
              <input className={`${field} w-full`} placeholder="Proxies; Privacy" value={v.beats} onChange={(e) => setV({ ...v, beats: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Outlet website</span>
              <input className={`${field} w-full`} placeholder="news.example.org" value={v.outlet_website} onChange={(e) => setV({ ...v, outlet_website: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Outlet name</span>
              <input className={`${field} w-full`} maxLength={200} placeholder="Example News" value={v.outlet_name} onChange={(e) => setV({ ...v, outlet_name: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Outlet tier</span>
              <select className={`${field} w-full`} disabled={!v.outlet_website.trim()} value={v.outlet_tier} onChange={(e) => setV({ ...v, outlet_tier: e.target.value as OutletTier | "" })}>
                <option value="">Not set</option>
                {tiers.map((t) => <option key={t} value={t}>{tierLabel[t]}</option>)}
              </select>
            </label>
            {active.length > 1 && (
              <label className="block">
                <span className="mb-1 block font-medium">Brand</span>
                <select className={`${field} w-full`} value={v.brand_id} onChange={(e) => setV({ ...v, brand_id: e.target.value })}>
                  {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                </select>
              </label>
            )}
          </div>
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !v.name.trim() || !v.email.trim()}>{busy ? "Adding…" : "Add journalist"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ImportJournalists({ ws, brand, onClose, onDone }: { ws: string; brand: { brand?: string }; onClose: () => void; onDone: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [check, setCheck] = useState<JournalistImportResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // First a dry run, which saves nothing, then the import itself.
  const send = async (dry: boolean) => {
    if (!file) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/journalists/import", {
      params: { path: { ws }, query: { ...brand, dry_run: dry } },
      body: await file.text(),
      bodySerializer: (b: string) => b,
      headers: { "Content-Type": "text/csv" },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    if (dry) return setCheck(data);
    toast(`Imported: ${data.created} new, ${data.updated} already here.${data.errors.length ? ` ${data.errors.length} rows couldn't be added.` : ""}`);
    onDone();
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <div className="space-y-3 text-sm">
          <DialogHeader>
            <DialogTitle>Import journalists</DialogTitle>
            <DialogDescription>
              A CSV with an email column, and any of name, title, outlet, outlet website, beats (separated by semicolons) and outlet tier (national, trade, regional, local or newsletter). People already here keep their records; their beats are added to.
            </DialogDescription>
          </DialogHeader>
          {error && <ErrorText>{error}</ErrorText>}
          <FilePicker file={file} onChange={(f) => { setFile(f); setCheck(null); }} accept=".csv,text/csv"
            example={{ name: "journalists-example.csv", content: "email,name,title,outlet,outlet website,beats,outlet tier\nana@news.example.org,Ana Diaz,Technology reporter,Example News,news.example.org,Proxies;Privacy,national\n" }} />
          {check && (
            <div className="space-y-1 rounded-md border p-3 text-[13px]">
              <div>{check.created} new, {check.updated} already here{check.duplicates ? `, ${check.duplicates} repeated rows merged` : ""}.</div>
              {check.errors.length > 0 && (
                <ul className="max-h-40 space-y-0.5 overflow-y-auto text-[12px] text-destructive">
                  {check.errors.slice(0, 50).map((e) => <li key={`${e.line}-${e.email ?? ""}`}>Line {e.line}: {e.error}</li>)}
                </ul>
              )}
            </div>
          )}
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            {check ? (
              <Button disabled={busy || check.created + check.updated === 0} onClick={() => send(false)}>{busy ? "Importing…" : `Import ${check.created + check.updated}`}</Button>
            ) : (
              <Button disabled={busy || !file} onClick={() => send(true)}>{busy ? "Checking…" : "Check the file"}</Button>
            )}
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function EditJournalist({ j, onClose, onSave }: { j: Journalist; onClose: () => void; onSave: (body: { beats: string[]; job_title: string | null }) => Promise<void> }) {
  const [beats, setBeats] = useState(j.beats.join("; "));
  const [title, setTitle] = useState(j.job_title ?? "");
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    await onSave({ beats: splitBeats(beats), job_title: title.trim() || null });
    setBusy(false);
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form className="space-y-3 text-sm" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{j.name || j.email}</DialogTitle>
            <DialogDescription>Your changes are kept: the news watch only suggests beats, and never changes ones you set.</DialogDescription>
          </DialogHeader>
          <label className="block">
            <span className="mb-1 block font-medium">Job title</span>
            <input className={`${field} w-full`} maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} />
          </label>
          <label className="block">
            <span className="mb-1 block font-medium">Beats <span className="font-normal text-muted-foreground">(separated by semicolons)</span></span>
            <input className={`${field} w-full`} value={beats} onChange={(e) => setBeats(e.target.value)} />
          </label>
          {j.suggested_beats.length > 0 && (
            <div className="flex flex-wrap items-center gap-1 text-[12px] text-muted-foreground">
              Suggested:
              {j.suggested_beats.map((b) => (
                <button key={b} type="button" className="rounded-full border border-dashed px-2 hover:text-foreground" onClick={() => setBeats(splitBeats(beats).concat(b).join("; "))}>+ {b}</button>
              ))}
            </div>
          )}
          <DialogFooter className="flex-row justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Searches({ ws, canManage, brand, go }: { ws: string; canManage: boolean; brand: { brand?: string }; go: (tab: Tab) => void }) {
  const { active, byId } = useBrand(ws);
  const [list, setList] = useState<NewsSearch[] | null>(null);
  const [draft, setDraft] = useState({ query: "", purpose: "coverage" as NewsSearch["purpose"], brand_id: "", time_range: "week" as NewsSearch["time_range"] });
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const forBrand = brand.brand ? active.find((b) => b.handle === brand.brand)?.id : draft.brand_id || active[0]?.id;

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/news-searches", { params: { path: { ws }, query: brand } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, reload, brand.brand]);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!draft.query.trim()) return;
    setBusy("add");
    const body = {
      query: draft.query.trim(),
      purpose: draft.purpose,
      time_range: draft.time_range,
      ...(draft.purpose === "coverage" || brand.brand ? { brand_id: forBrand } : {}),
    };
    const { error } = await api.POST("/v1/workspaces/{ws}/news-searches", { params: { path: { ws } }, body });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setDraft({ ...draft, query: "" });
    toast("Saved. Its first run starts now.");
    setReload((n) => n + 1);
  };
  const run = async (s: NewsSearch) => {
    setBusy(s.id);
    const { error } = await api.POST("/v1/workspaces/{ws}/news-searches/{search}/run", { params: { path: { ws, search: s.id } } });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    toast("Running. New articles show in a minute.");
  };
  const patch = async (s: NewsSearch, body: { status?: NewsSearch["status"]; check_every_hours?: number }) => {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/news-searches/{search}", { params: { path: { ws, search: s.id } }, body });
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const remove = async (s: NewsSearch) => {
    if (!(await confirmAction(`Delete "${s.name}"?`, "The articles only it found go too. Coverage it found stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/news-searches/{search}", { params: { path: { ws, search: s.id } } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };
  const state = (s: NewsSearch) =>
    s.status === "paused" ? "Paused" : s.last_error_code ? `Failed: ${errorLabel[s.last_error_code] ?? s.last_error_code}` : s.last_checked_at ? `Ran ${ago(s.last_checked_at)}` : "Waiting to run";

  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const needsConnection = list.some((s) => s.last_error_code === "connector_needed");
  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      {needsConnection && (
        <Empty title="Connect a web search" action={<Button onClick={() => go("connectors")}>Connect Geonode</Button>}>
          Searches run through your own Geonode account, one search each time they run. Until it's connected they wait.
        </Empty>
      )}
      {canManage && (
        <form onSubmit={save} className="flex flex-wrap items-center gap-2">
          <select className={field} aria-label="Kind" value={draft.purpose} onChange={(e) => setDraft({ ...draft, purpose: e.target.value as NewsSearch["purpose"] })}>
            <option value="coverage">Your brand's coverage</option>
            <option value="topic">A topic</option>
          </select>
          <input
            className={`${field} w-64 max-w-full`}
            aria-label="Search for"
            placeholder={draft.purpose === "coverage" ? (active.find((b) => b.id === forBrand)?.name ?? "Your brand's name") : "residential proxies"}
            value={draft.query}
            onChange={(e) => setDraft({ ...draft, query: e.target.value })}
            maxLength={500}
          />
          {!brand.brand && draft.purpose === "coverage" && active.length > 1 && (
            <select className={field} aria-label="Brand" value={forBrand} onChange={(e) => setDraft({ ...draft, brand_id: e.target.value })}>
              {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
          )}
          <select className={field} aria-label="Looks back" value={draft.time_range} onChange={(e) => setDraft({ ...draft, time_range: e.target.value as NewsSearch["time_range"] })}>
            <option value="day">Last day</option>
            <option value="week">Last week</option>
            <option value="month">Last month</option>
          </select>
          <Button type="submit" disabled={!draft.query.trim() || busy === "add" || (draft.purpose === "coverage" && !forBrand)}>Save search</Button>
        </form>
      )}
      <DataTable
        data={list}
        rowId={(s) => s.id}
        searchPlaceholder="Search saved searches"
        empty="No searches yet. Save one for your brand's name to see what's written about you."
        mobile={(s) => (
          <div className="space-y-1">
            <div className="flex items-start justify-between gap-2">
              <span className="font-medium">{s.name}</span>
              <Badge tone={s.purpose === "coverage" ? "green" : "blue"}>{s.purpose === "coverage" ? "Coverage" : "Topic"}</Badge>
            </div>
            <div className="text-xs text-muted-foreground">{[byId(s.brand_id)?.name, `${s.articles} articles`, state(s)].filter(Boolean).join(" · ")}</div>
          </div>
        )}
        columns={[
          {
            id: "name",
            header: "Search",
            accessorFn: (s) => `${s.name} ${s.query}`,
            cell: ({ row: { original: s } }) => (
              <div className="min-w-0 space-y-0.5">
                <div className="font-medium">{s.name}</div>
                {s.name !== s.query && <div className="max-w-72 truncate font-mono text-[12px] text-muted-foreground">{s.query}</div>}
              </div>
            ),
          },
          { id: "kind", header: "Kind", accessorFn: (s) => s.purpose, cell: ({ row: { original: s } }) => <Badge tone={s.purpose === "coverage" ? "green" : "blue"}>{s.purpose === "coverage" ? `Coverage${byId(s.brand_id) ? ` of ${byId(s.brand_id)?.name}` : ""}` : "Topic"}</Badge> },
          { id: "articles", header: "Articles", accessorFn: (s) => s.articles, meta: { align: "right", mono: true }, cell: ({ row }) => row.original.articles.toLocaleString() },
          { id: "every", header: "Runs", accessorFn: (s) => s.check_every_hours, cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">every {row.original.check_every_hours === 24 ? "day" : row.original.check_every_hours === 168 ? "week" : `${row.original.check_every_hours} hours`}</span> },
          { id: "state", header: "Last run", accessorFn: (s) => s.last_checked_at ?? "", cell: ({ row }) => <span className="text-[12px]">{state(row.original)}</span> },
          {
            id: "actions",
            header: () => <span className="sr-only">Actions</span>,
            enableSorting: false,
            meta: { align: "right" },
            cell: ({ row: { original: s } }) =>
              canManage && (
                <div className="flex items-center justify-end gap-1">
                  {s.status === "active" && <Button variant="ghost" disabled={busy === s.id} onClick={() => run(s)}>Run now</Button>}
                  <Menu
                    aria={`More for ${s.name}`}
                    items={[
                      { label: s.status === "active" ? "Pause" : "Resume", onClick: () => patch(s, { status: s.status === "active" ? "paused" : "active" }) },
                      s.check_every_hours !== 24 && { label: "Run every day", onClick: () => patch(s, { check_every_hours: 24 }) },
                      s.check_every_hours !== 6 && { label: "Run every 6 hours", onClick: () => patch(s, { check_every_hours: 6 }) },
                      s.check_every_hours !== 168 && { label: "Run every week", onClick: () => patch(s, { check_every_hours: 168 }) },
                      { label: "Delete", onClick: () => remove(s), danger: true },
                    ]}
                  />
                </div>
              ),
          },
        ]}
      />
    </div>
  );
}
