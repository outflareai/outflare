import { useEffect, useState } from "react";
import type { Coverage, Journalist, NewsArticle, NewsSearch } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// News watch: saved searches that run on a schedule through the
// workspace's own web search connection (Geonode), the articles they find,
// and coverage: articles about a brand, each on its outlet's company
// record, to confirm or dismiss; and journalists: the articles' authors
// whose address was found, as person records, and media lists of them
// (smart lists with a news search rule). The first part of digital PR.

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
      {section === "journalists" && <Journalists ws={ws} canLists={canLists} brand={brand} />}
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

function Journalists({ ws, canLists, brand }: { ws: string; canLists: boolean; brand: { brand?: string } }) {
  const [list, setList] = useState<Journalist[] | null>(null);
  const [searches, setSearches] = useState<NewsSearch[]>([]);
  const [search, setSearch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/news-searches", { params: { path: { ws }, query: brand } }).then(({ data }) => setSearches(data?.data ?? []));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand.brand]);
  useEffect(() => {
    setList(null);
    api.GET("/v1/workspaces/{ws}/journalists", { params: { path: { ws }, query: { ...brand, ...(search ? { search_id: search } : {}), limit: 200 } } })
      .then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setError("");
        setList(data.data);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, search, brand.brand]);

  // A media list is a smart list of whoever wrote about the search's topic
  // in the last year; it keeps itself up to date.
  const mediaList = async () => {
    const s = searches.find((x) => x.id === search);
    if (!s) return;
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/smart-lists", {
      params: { path: { ws } },
      body: { name: `Journalists: ${s.name}`.slice(0, 200), description: `People who wrote an article the "${s.name}" news search found in the last year.`.slice(0, 2000),
        rules: [{ news_search: s.id, op: "exists", within_days: 365 }] },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast("Media list made. It's under Lists and keeps itself up to date.");
  };
  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <div className="space-y-2">
      {error && <ErrorText>{error}</ErrorText>}
      <p className="text-[12px] text-muted-foreground">
        The authors of the articles your searches found, once the free email finder found their address at the outlet. Each is a person record on the outlet's company.
      </p>
      <DataTable
        data={list}
        rowId={(j) => j.record_id}
        searchPlaceholder="Search journalists"
        toolbar={
          searches.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <select className={field} aria-label="Topic" value={search} onChange={(e) => setSearch(e.target.value)}>
                <option value="">Every topic</option>
                {searches.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
              {canLists && search && <Button variant="ghost" disabled={busy} onClick={mediaList}>Make a media list</Button>}
            </div>
          )
        }
        empty={searches.length === 0 ? "No journalists yet. Save a search under Searches; its articles' authors show here once their addresses are found." :
          "No journalists yet. They show once a search's articles have been read and their authors' addresses found."}
        mobile={(j) => (
          <div className="space-y-1">
            <div className="font-medium">{j.name || j.email}</div>
            <div className="text-xs text-muted-foreground">{[j.outlets.join(", "), `${j.articles} article${j.articles === 1 ? "" : "s"}`].join(" · ")}</div>
            <div className="text-[12px]">{j.latest_article.title}</div>
          </div>
        )}
        columns={[
          {
            id: "name",
            header: "Journalist",
            accessorFn: (j) => `${j.name} ${j.email ?? ""}`,
            cell: ({ row: { original: j } }) => (
              <div className="min-w-0 space-y-0.5">
                <div className="font-medium">{j.name || j.email}</div>
                {j.email && <div className="font-mono text-[12px] text-muted-foreground" title={`The email finder was ${j.confidence}% sure`}>{j.email}</div>}
              </div>
            ),
          },
          { id: "outlets", header: "Writes for", accessorFn: (j) => j.outlets.join(", "), cell: ({ row }) => <span className="text-[12px]">{row.original.outlets.join(", ")}</span> },
          { id: "articles", header: "Articles", accessorFn: (j) => j.articles, meta: { align: "right", mono: true }, cell: ({ row }) => row.original.articles },
          {
            id: "latest",
            header: "Latest",
            accessorFn: (j) => j.latest_article.published_at ?? "",
            cell: ({ row: { original: j } }) => (
              <div className="min-w-0 space-y-0.5">
                <a className="block max-w-80 truncate text-[13px] underline-offset-2 hover:underline" href={j.latest_article.url} target="_blank" rel="noreferrer noopener nofollow" title={j.latest_article.title}>{j.latest_article.title || path(j.latest_article.url)}</a>
                {j.latest_article.published_at && <div className="text-[11px] text-muted-foreground">{day(j.latest_article.published_at)}</div>}
              </div>
            ),
          },
          { id: "topics", header: "Topics", accessorFn: (j) => j.topics.map((t) => t.name).join(", "), cell: ({ row }) => <span className="text-[12px]">{row.original.topics.map((t) => t.name).join(", ")}</span> },
        ]}
      />
    </div>
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
