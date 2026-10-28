import { useEffect, useState } from "react";
import type { LinkBuildingSettings, LinkProspect, LinkRead, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Link building: one list of pages that could link to an SEO site, from
// every way of earning links, followed from prospect to a live link; one
// website is pitched at a time. Dead pages and Top pages read who links to
// competitors' pages from the workspace's own Ahrefs or DataForSEO (paid:
// the cost is shown and confirmed first), and their linking pages can be
// added to the list. Pitches are sent from sequences.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "prospects" | "dead" | "top" | "settings";
type Stage = LinkProspect["stage"];
type Method = LinkProspect["method"];
type Show = "open" | "outreach" | "live" | "ended" | "excluded";
type DeadPage = { url: string; competitor_id: string; host: string; gone_at: string; read_id: string | null; links: number | null; referring_domains: number | null; best_rating: number | null };
type TopPage = { url: string; links: number; referring_domains: number; best_rating: number | null };

const stageLabel: Record<Stage, string> = {
  prospect: "To pitch", excluded: "Excluded", pitched: "Pitched", replied: "Replied", agreed: "Agreed", live: "Live", declined: "Declined", removed: "Link removed",
};
const stageTone: Record<Stage, Tone> = {
  prospect: "blue", excluded: "grey", pitched: "amber", replied: "amber", agreed: "green", live: "green", declined: "grey", removed: "red",
};
const methodLabel: Record<Method, string> = {
  competitor_gap: "Competitor gap", broken_link: "Broken link", reclaim: "Win back", competitor_dead_page: "Competitor's dead page",
  resource_page: "Resource page", asset: "Content", unlinked_mention: "Mention", partner: "Partner", manual: "Added by hand",
};
const reasonLabel: Record<NonNullable<LinkProspect["excluded_reason"]>, string> = {
  links_to_us: "already links to you", competitor: "a competitor", customer: "a customer", partner: "a partner", opted_out: "opted out", spam: "spam",
};
const shows: Record<Show, { label: string; stages: Stage[] }> = {
  open: { label: "To pitch", stages: ["prospect"] },
  outreach: { label: "In outreach", stages: ["pitched", "replied", "agreed"] },
  live: { label: "Live", stages: ["live"] },
  ended: { label: "Declined or removed", stages: ["declined", "removed"] },
  excluded: { label: "Excluded", stages: ["excluded"] },
};
const moves: Partial<Record<Stage, Stage[]>> = {
  prospect: ["pitched"], pitched: ["replied", "agreed", "declined"], replied: ["agreed", "declined"], agreed: ["live", "declined"],
  live: ["removed"], declined: ["prospect"], removed: ["prospect"], excluded: ["prospect"],
};
const moveLabel: Record<Stage, string> = {
  prospect: "Back to the list", excluded: "Exclude", pitched: "Mark pitched", replied: "Mark replied", agreed: "Mark agreed", live: "Mark live",
  declined: "Mark declined", removed: "Mark link removed",
};
const num = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: 0 }));
const cost = (unit: string, v: number) => (unit === "USD" ? `$${v.toFixed(2)}` : `${Math.round(v).toLocaleString()} API units`);
const path = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

export function LinkBuilding({ ws, canManage, go }: Props) {
  const [sites, setSites] = useState<SEOSite[] | null>(null);
  const [site, setSite] = useState("");
  const [section, setSection] = useState<Section>("prospects");
  const [error, setError] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};

  useEffect(() => {
    setSites(null);
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brand } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setSites(data.data);
      setSite((s) => (data.data.some((x) => x.id === s) ? s : (data.data[0]?.id ?? "")));
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey]);

  const current = sites?.find((s) => s.id === site);
  const header = (
    <PageHeader
      title="Link building"
      description="Pages that could link to your site, from every way of earning links, followed until the link is live. One website is pitched at a time. Agents use list_link_prospects, read_links and list_dead_pages."
      action={
        sites && sites.length > 1 ? (
          <select className={field} aria-label="Site" value={site} onChange={(e) => setSite(e.target.value)}>
            {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
          </select>
        ) : undefined
      }
    />
  );
  if (error && !sites) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sites) return <div className="space-y-6">{header}<Loading /></div>;
  if (sites.length === 0 || !current)
    return (
      <div className="space-y-6">
        {header}
        <Empty title="No website yet" action={<Button onClick={() => go("audits")}>Add your website</Button>}>
          Link prospects are kept for the websites you audit. Add yours under Site audits first.
        </Empty>
      </div>
    );
  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      <Tabs value={section} onValueChange={(v) => setSection(v as Section)} className="max-w-full overflow-x-auto">
        <TabsList className="h-8">
          <TabsTrigger value="prospects" className="px-3 text-[12px]">Prospects</TabsTrigger>
          <TabsTrigger value="dead" className="px-3 text-[12px]">Competitors' dead pages</TabsTrigger>
          <TabsTrigger value="top" className="px-3 text-[12px]">Most linked pages</TabsTrigger>
          <TabsTrigger value="settings" className="px-3 text-[12px]">Settings</TabsTrigger>
        </TabsList>
      </Tabs>
      {section === "prospects" && <Prospects ws={ws} site={current} canManage={canManage} brand={brand} />}
      {section === "dead" && <DeadPages ws={ws} site={current} canManage={canManage} brand={brand} go={go} />}
      {section === "top" && <TopPages ws={ws} site={current} canManage={canManage} />}
      {section === "settings" && <Settings ws={ws} canManage={canManage} />}
    </div>
  );
}

// readFirst estimates a read, asks to spend it, then reads. It answers the
// read, or null when the person said no or it failed (with the error set).
async function readFirst(ws: string, body: { target: string; site_id?: string }, setError: (e: string) => void): Promise<LinkRead | null> {
  const est = await api.POST("/v1/workspaces/{ws}/link-reads", { params: { path: { ws } }, body: { ...body, estimate_only: true } });
  if (est.error || !est.data) {
    setError(errorMessage(est.error));
    return null;
  }
  const e = est.data as { estimate: number; budget: { unit: string; spent: number; limit: number; unlimited: boolean } };
  const left = e.budget.unlimited ? "no monthly limit" : `${cost(e.budget.unit, Math.max(0, e.budget.limit - e.budget.spent))} left this month`;
  if (!(await confirmAction(`Read who links to ${body.target}?`, `About ${cost(e.budget.unit, e.estimate)} on your own SEO connection (${left}). A read from the last day is free.`, "Read links"))) return null;
  const { data, error } = await api.POST("/v1/workspaces/{ws}/link-reads", { params: { path: { ws } }, body });
  if (error || !data) {
    setError(errorMessage(error));
    return null;
  }
  setError("");
  return data as LinkRead;
}

// addFromRead adds a read's linking pages to the prospects list.
async function addFromRead(ws: string, site: string, readID: string, method: Method, evidence: Record<string, string>, setError: (e: string) => void) {
  const { data, error } = await api.GET("/v1/workspaces/{ws}/link-reads/{read}", { params: { path: { ws, read: readID } } });
  if (error || !data) return setError(errorMessage(error));
  const rows = (data.rows ?? []).filter((r) => !r.lost).slice(0, 500);
  if (rows.length === 0) return toast("No live links to add.");
  const res = await api.POST("/v1/workspaces/{ws}/link-prospects", {
    params: { path: { ws } },
    body: {
      site_id: site,
      source: "link_read",
      prospects: rows.map((r) => ({
        page_url: r.source_url, method, evidence: { ...evidence, read_id: readID, links_to: r.target_url },
        domain_rating: r.source_rating ?? undefined, traffic: r.domain_traffic ?? undefined, spam_score: r.spam_score ?? undefined,
      })),
    },
  });
  if (res.error || !res.data) return setError(errorMessage(res.error));
  setError("");
  toast(`Added ${res.data.added} prospect${res.data.added === 1 ? "" : "s"}${res.data.excluded ? `, ${res.data.excluded} kept out` : ""}${res.data.existing ? `; ${res.data.existing} ${res.data.existing === 1 ? "was" : "were"} already listed` : ""}.`);
}

function Prospects({ ws, site, canManage, brand }: { ws: string; site: SEOSite; canManage: boolean; brand: { brand?: string } }) {
  const [show, setShow] = useState<Show>("open");
  const [list, setList] = useState<LinkProspect[] | null>(null);
  const [page, setPage] = useState("");
  const [method, setMethod] = useState<Method>("manual");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    setList(null);
    api.GET("/v1/workspaces/{ws}/link-prospects", { params: { path: { ws }, query: { ...brand, site_id: site.id, stage: shows[show].stages.join(","), limit: 500 } } })
      .then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setList(data.data);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, site.id, show, reload, brand.brand]);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!page.trim()) return;
    setBusy("add");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/link-prospects", { params: { path: { ws } }, body: { site_id: site.id, prospects: [{ page_url: page.trim(), method }] } });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setPage("");
    const p = data.data[0];
    if (data.existing) toast("That page is already on the list.");
    else if (p?.stage === "excluded" && p.excluded_reason) toast(`Added, but kept out of outreach: ${p.domain} is ${reasonLabel[p.excluded_reason]}.`);
    else toast(`Added ${p?.domain ?? "it"}, scored ${p?.score ?? 0}.`);
    setReload((n) => n + 1);
  };
  const move = async (p: LinkProspect, stage: Stage) => {
    let body: { stage: Stage; excluded_reason?: NonNullable<LinkProspect["excluded_reason"]> } = { stage };
    if (stage === "excluded") {
      const r = await ask({
        title: `Keep ${p.domain} out of outreach?`,
        body: "Say why, so nobody adds it again by mistake.",
        choices: [
          { label: "Cancel", value: "cancel", variant: "ghost" },
          { label: "Opted out", value: "opted_out" },
          { label: "Already links to us", value: "links_to_us" },
          { label: "Spam", value: "spam" },
        ],
      });
      if (!r || r.choice === "cancel") return;
      body = { stage, excluded_reason: r.choice as NonNullable<LinkProspect["excluded_reason"]> };
    }
    setBusy(p.id);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/link-prospects/{prospect}", { params: { path: { ws, prospect: p.id } }, body });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const offer = async (p: LinkProspect) => {
    const r = await ask({
      title: `Your page to offer ${p.domain}`,
      body: `A page on ${site.host} that fits theirs, such as the replacement for the broken link.`,
      input: { label: "Page", placeholder: `https://${site.host}/guide`, value: p.target_url, maxLength: 2048, optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/link-prospects/{prospect}", { params: { path: { ws, prospect: p.id } }, body: { target_url: r.value.trim() } });
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const remove = async (p: LinkProspect) => {
    if (!(await confirmAction(`Delete ${p.page_url}?`, "For a page added by mistake. To keep the website out of outreach, exclude it instead.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/link-prospects/{prospect}", { params: { path: { ws, prospect: p.id } } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };

  const actions = (p: LinkProspect) =>
    canManage && (
      <div className="flex items-center justify-end gap-1">
        {p.stage === "prospect" && <Button variant="ghost" disabled={busy === p.id} onClick={() => move(p, "pitched")}>Mark pitched</Button>}
        <Menu
          aria={`More for ${p.page_url}`}
          items={[
            ...(moves[p.stage] ?? []).filter((s) => !(p.stage === "prospect" && s === "pitched")).map((s) => ({ label: moveLabel[s], onClick: () => move(p, s) })),
            p.stage !== "excluded" && { label: "Exclude", onClick: () => move(p, "excluded") },
            { label: p.target_url ? "Change your page to offer" : "Choose your page to offer", onClick: () => offer(p) },
            { label: "Delete", onClick: () => remove(p), danger: true },
          ]}
        />
      </div>
    );
  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      {canManage && (
        <form onSubmit={add} className="flex flex-wrap items-center gap-2">
          <input className={`${field} w-80 max-w-full`} aria-label="Page that could link" placeholder="https://blog.example.org/best-tools" value={page} onChange={(e) => setPage(e.target.value)} maxLength={2048} />
          <select className={field} aria-label="How you found it" value={method} onChange={(e) => setMethod(e.target.value as Method)}>
            {(Object.keys(methodLabel) as Method[]).map((m) => <option key={m} value={m}>{methodLabel[m]}</option>)}
          </select>
          <Button type="submit" disabled={!page.trim() || busy === "add"}>Add page</Button>
        </form>
      )}
      {!list ? (
        <Loading />
      ) : (
        <DataTable
          data={list}
          rowId={(p) => p.id}
          searchPlaceholder="Search pages"
          toolbar={
            <select className={field} aria-label="Show" value={show} onChange={(e) => setShow(e.target.value as Show)}>
              {(Object.keys(shows) as Show[]).map((s) => <option key={s} value={s}>{shows[s].label}</option>)}
            </select>
          }
          empty={show === "open" ? "Nothing to pitch yet. Add a page above, or add the sites linking to a competitor's dead page." : "Nothing here."}
          mobile={(p) => (
            <div className="space-y-1">
              <div className="flex items-start justify-between gap-2">
                <span className="font-medium break-all">{p.domain}</span>
                <Badge tone={stageTone[p.stage]}>{stageLabel[p.stage]}</Badge>
              </div>
              <div className="font-mono text-[12px] break-all text-muted-foreground">{path(p.page_url)}</div>
              <div className="text-xs text-muted-foreground">{[`score ${p.score}`, methodLabel[p.method], p.domain_rating != null && `rating ${num(p.domain_rating)}`].filter(Boolean).join(" · ")}</div>
              {actions(p)}
            </div>
          )}
          columns={[
            {
              id: "page",
              header: "Page",
              accessorFn: (p) => `${p.page_url} ${p.note}`,
              cell: ({ row: { original: p } }) => (
                <div className="min-w-0 space-y-0.5">
                  <a className="font-medium whitespace-nowrap underline-offset-2 hover:underline" href={p.page_url} target="_blank" rel="noreferrer noopener nofollow">{p.domain}</a>
                  <div className="max-w-72 truncate font-mono text-[12px] text-muted-foreground" title={p.page_url}>{path(p.page_url)}</div>
                  {p.target_url && <div className="max-w-72 truncate text-[12px] text-muted-foreground" title={p.target_url}>Offer {path(p.target_url)}</div>}
                </div>
              ),
            },
            { id: "method", header: "Found by", accessorFn: (p) => p.method, cell: ({ row }) => <span className="text-[12px]">{methodLabel[row.original.method]}</span> },
            {
              id: "score",
              header: "Score",
              accessorFn: (p) => p.score,
              meta: { align: "right", mono: true },
              cell: ({ row: { original: p } }) => <span title={p.score_factors.map((f) => `${f.code} ${f.points > 0 ? "+" : ""}${f.points}`).join(", ")}>{p.score}</span>,
            },
            { id: "rating", header: "Rating", accessorFn: (p) => p.domain_rating ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.domain_rating) },
            { id: "traffic", header: "Traffic", accessorFn: (p) => p.traffic ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.traffic) },
            {
              id: "stage",
              header: "Stage",
              accessorFn: (p) => p.stage,
              cell: ({ row: { original: p } }) => (
                <div className="space-y-0.5">
                  <Badge tone={stageTone[p.stage]} title={p.excluded_reason ? reasonLabel[p.excluded_reason] : undefined}>{stageLabel[p.stage]}</Badge>
                  <div className="text-[11px] text-muted-foreground">{p.excluded_reason ? reasonLabel[p.excluded_reason] : ago(p.stage_changed_at)}</div>
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

function DeadPages({ ws, site, canManage, brand, go }: { ws: string; site: SEOSite; canManage: boolean; brand: { brand?: string }; go: (tab: Tab) => void }) {
  const [list, setList] = useState<DeadPage[] | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/link-reads/dead-pages", { params: { path: { ws }, query: { ...brand, site_id: site.id, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data as DeadPage[]);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, site.id, reload, brand.brand]);

  const read = async (d: DeadPage) => {
    setBusy(d.url);
    const r = await readFirst(ws, { target: d.url, site_id: site.id }, setError);
    setBusy("");
    if (r) {
      toast(`${r.referring_domains} site${r.referring_domains === 1 ? "" : "s"} link to it.`);
      setReload((n) => n + 1);
    }
  };
  const addAll = async (d: DeadPage) => {
    if (!d.read_id) return;
    setBusy(d.url);
    await addFromRead(ws, site.id, d.read_id, "competitor_dead_page", { dead_url: d.url, competitor_id: d.competitor_id }, setError);
    setBusy("");
  };
  if (!list) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <div className="space-y-2">
      {error && <ErrorText>{error}</ErrorText>}
      <p className="text-[12px] text-muted-foreground">
        Pages gone from your tracked competitors' sitemaps in the last 90 days. The sites still linking to them now have a broken link and may link to your page instead. Each week the SEO snapshot reads who links to new ones, within the share of your budget set under Settings.
      </p>
      <DataTable
        data={list}
        rowId={(d) => d.url}
        searchPlaceholder="Search pages"
        empty={
          <span>
            No dead pages yet. They show once a tracked competitor removes a page from its sitemaps.{" "}
            <button className="underline" onClick={() => go("competitors")}>Track competitors</button>
          </span>
        }
        mobile={(d) => (
          <div className="space-y-1">
            <span className="font-mono text-[12px] break-all">{d.url}</span>
            <div className="text-xs text-muted-foreground">{d.read_id ? `${num(d.referring_domains)} linking sites · best rating ${num(d.best_rating)}` : "Not read yet"} · gone {ago(d.gone_at)}</div>
          </div>
        )}
        columns={[
          {
            id: "url",
            header: "Dead page",
            accessorFn: (d) => d.url,
            cell: ({ row: { original: d } }) => (
              <div className="min-w-0 space-y-0.5">
                <div className="font-medium">{d.host}</div>
                <div className="max-w-80 truncate font-mono text-[12px] text-muted-foreground" title={d.url}>{path(d.url)}</div>
              </div>
            ),
          },
          { id: "gone", header: "Gone", accessorFn: (d) => d.gone_at, cell: ({ row }) => <span className="text-[12px] whitespace-nowrap">{ago(row.original.gone_at)}</span> },
          { id: "sites", header: "Linking sites", accessorFn: (d) => d.referring_domains ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.read_id ? num(row.original.referring_domains) : "–") },
          { id: "best", header: "Best rating", accessorFn: (d) => d.best_rating ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.best_rating) },
          {
            id: "actions",
            header: () => <span className="sr-only">Actions</span>,
            enableSorting: false,
            meta: { align: "right" },
            cell: ({ row: { original: d } }) =>
              canManage && (
                <div className="flex justify-end gap-1">
                  {!d.read_id && <Button variant="ghost" disabled={busy === d.url} onClick={() => read(d)}>Read links</Button>}
                  {d.read_id && (d.referring_domains ?? 0) > 0 && <Button variant="ghost" disabled={busy === d.url} onClick={() => addAll(d)}>Add as prospects</Button>}
                </div>
              ),
          },
        ]}
      />
    </div>
  );
}

function TopPages({ ws, site, canManage }: { ws: string; site: SEOSite; canManage: boolean }) {
  const [target, setTarget] = useState("");
  const [shown, setShown] = useState("");
  const [data, setData] = useState<{ read_id: string | null; read_at: string | null; data: TopPage[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = async (domain: string) => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/link-reads/top-pages", { params: { path: { ws }, query: { target: domain, limit: 100 } } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setShown(domain);
    setData(data as { read_id: string | null; read_at: string | null; data: TopPage[] });
  };
  const show = async (e: React.FormEvent) => {
    e.preventDefault();
    if (target.trim()) await load(target.trim());
  };
  const read = async () => {
    setBusy(true);
    const r = await readFirst(ws, { target: shown, site_id: site.id }, setError);
    setBusy(false);
    if (r) await load(shown);
  };
  return (
    <div className="space-y-3">
      {error && <ErrorText>{error}</ErrorText>}
      <p className="text-[12px] text-muted-foreground">A domain's pages ranked by the sites linking to them: the pages worth making a better version of. Counts cover the strongest 100 links of the latest read.</p>
      <form onSubmit={show} className="flex flex-wrap items-center gap-2">
        <input className={`${field} w-64 max-w-full`} aria-label="Domain" placeholder="rival.example" value={target} onChange={(e) => setTarget(e.target.value)} maxLength={300} />
        <Button type="submit" disabled={!target.trim()}>Show</Button>
      </form>
      {data && (
        <>
          <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
            <span>{data.read_at ? `From the read ${ago(data.read_at)}.` : `${shown} hasn't been read yet.`}</span>
            {canManage && <Button variant="ghost" disabled={busy} onClick={read}>{data.read_at ? "Read again" : "Read its links"}</Button>}
          </div>
          {data.data.length > 0 && (
            <DataTable
              data={data.data}
              rowId={(p) => p.url}
              searchPlaceholder="Search pages"
              empty="Nothing here."
              mobile={(p) => (
                <div className="space-y-1">
                  <span className="font-mono text-[12px] break-all">{p.url}</span>
                  <div className="text-xs text-muted-foreground">{num(p.referring_domains)} linking sites · best rating {num(p.best_rating)}</div>
                </div>
              )}
              columns={[
                {
                  id: "url",
                  header: "Page",
                  accessorFn: (p) => p.url,
                  cell: ({ row }) => (
                    <a className="max-w-96 truncate font-mono text-[12px] underline-offset-2 hover:underline" href={row.original.url} target="_blank" rel="noreferrer noopener nofollow" title={row.original.url}>{path(row.original.url)}</a>
                  ),
                },
                { id: "sites", header: "Linking sites", accessorFn: (p) => p.referring_domains, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.referring_domains) },
                { id: "links", header: "Links", accessorFn: (p) => p.links, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.links) },
                { id: "best", header: "Best rating", accessorFn: (p) => p.best_rating ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.best_rating) },
              ]}
            />
          )}
        </>
      )}
    </div>
  );
}

function Settings({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [set, setSet] = useState<LinkBuildingSettings | null>(null);
  const [draft, setDraft] = useState({ cooldown: "60", weekly: true, percent: "10" });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/link-building/settings", { params: { path: { ws } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setSet(data);
      setDraft({ cooldown: String(data.pitch_cooldown_days), weekly: data.weekly_reads, percent: String(data.weekly_budget_percent) });
    });
  }, [ws]);
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/link-building/settings", {
      params: { path: { ws } },
      body: { pitch_cooldown_days: Number(draft.cooldown), weekly_reads: draft.weekly, weekly_budget_percent: Number(draft.percent) },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setSet(data);
    toast("Saved.");
  };
  if (!set) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <form onSubmit={save} className="max-w-xl space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      <label className="block space-y-1">
        <span className="text-[13px] font-medium">Wait after a no</span>
        <span className="block text-[12px] text-muted-foreground">Days after a website declines a pitch before any campaign can pitch it again.</span>
        <input className={`${field} w-24`} type="number" min={0} max={365} value={draft.cooldown} disabled={!canManage} onChange={(e) => setDraft({ ...draft, cooldown: e.target.value })} />
      </label>
      <label className="flex items-start gap-2">
        <input type="checkbox" className="mt-1" checked={draft.weekly} disabled={!canManage} onChange={(e) => setDraft({ ...draft, weekly: e.target.checked })} />
        <span>
          <span className="block text-[13px] font-medium">Read links to competitors' dead pages each week</span>
          <span className="block text-[12px] text-muted-foreground">With the weekly SEO snapshot, on your own Ahrefs or DataForSEO: at most 20 new dead pages a site, each read once.</span>
        </span>
      </label>
      <label className="block space-y-1">
        <span className="text-[13px] font-medium">Most of the monthly SEO budget to spend on it</span>
        <span className="flex items-center gap-1">
          <input className={`${field} w-20`} type="number" min={1} max={100} value={draft.percent} disabled={!canManage || !draft.weekly} onChange={(e) => setDraft({ ...draft, percent: e.target.value })} />
          <span className="text-[13px]">%</span>
        </span>
      </label>
      {canManage && <Button type="submit" disabled={busy}>Save</Button>}
    </form>
  );
}
