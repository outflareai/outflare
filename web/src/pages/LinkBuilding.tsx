import { useEffect, useState } from "react";
import type { LinkBuildingSettings, LinkPitch, LinkProspect, LinkRead, LinkResultRow, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Link building: one list of pages that could link to an SEO site, from
// every way of earning links, followed from prospect to a live link; one
// website is pitched at a time. Dead pages and Top pages read who links to
// competitors' pages from the workspace's own Ahrefs or DataForSEO (paid:
// the cost is shown and confirmed first), and their linking pages can be
// added to the list. A pitch goes out through one of Outflare's own
// sequences, with an opener the workspace's AI wrote for the page as the
// step's {{custom.opener}}; it waits for a person to approve it unless the
// sequence skips approval. Replies and a daily check of the page move the
// prospect on to a live link.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "prospects" | "pitches" | "results" | "dead" | "top" | "settings";
type LinkSequence = { sequence_id: string; name: string; status: string; brand_id: string | null; approve_pitches: boolean; uses_opener: boolean; pitches: number };
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
  page_gone: "its page is gone",
};
type Check = NonNullable<LinkProspect["check"]>;
const checkLabel: Record<Check["status"], string> = {
  up: "Page up", gone: "Page gone", redirected: "Redirects away", blocked: "robots.txt blocks OutflareBot", opted_out: "Site opted out of crawling",
  unreachable: "Site unreachable", failed: "Check failed",
};
const sourceLabel: Record<LinkProspect["contacts"][number]["source"], string> = { author: "Author", page: "On the page", contact_page: "Contact page" };
const checkSummary = (c: Check) =>
  [checkLabel[c.status], c.links_to_us && "links to you", c.broken.length > 0 && `${c.broken.length} broken link${c.broken.length === 1 ? "" : "s"}`, c.fit != null && `fit ${c.fit}`]
    .filter(Boolean)
    .join(" · ");
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
type PitchStatus = LinkPitch["status"];
const pitchLabel: Record<PitchStatus, string> = { writing: "Writing", draft: "To approve", enrolled: "Sent", rejected: "Rejected", failed: "Failed" };
const pitchTone: Record<PitchStatus, Tone> = { writing: "grey", draft: "amber", enrolled: "green", rejected: "grey", failed: "red" };
const replyLabel: Record<NonNullable<LinkPitch["reply_kind"]>, string> = {
  agreed: "Agreed", declined: "Declined", wants_payment: "Asks for payment", wants_content: "Wants content first", other: "Replied",
};
const draftReason: Record<string, string> = {
  no_ai: "Connect your AI model to have it write openers; until then, write one yourself.",
  ai_unavailable: "Your AI model couldn't be used (its limit may be reached). Write the opener yourself.",
  ai_failed: "The AI couldn't write an opener for this page. Write it yourself.",
  site_in_outreach: "Another page on this website is in outreach or cooling down, so it waits for you.",
  opted_out: "The contact or website is suppressed.",
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
          <TabsTrigger value="pitches" className="px-3 text-[12px]">Pitches</TabsTrigger>
          <TabsTrigger value="results" className="px-3 text-[12px]">Results</TabsTrigger>
          <TabsTrigger value="dead" className="px-3 text-[12px]">Competitors' dead pages</TabsTrigger>
          <TabsTrigger value="top" className="px-3 text-[12px]">Most linked pages</TabsTrigger>
          <TabsTrigger value="settings" className="px-3 text-[12px]">Settings</TabsTrigger>
        </TabsList>
      </Tabs>
      {section === "prospects" && <Prospects ws={ws} site={current} canManage={canManage} brand={brand} />}
      {section === "pitches" && <Pitches ws={ws} canManage={canManage} brand={brand} />}
      {section === "results" && <Results ws={ws} site={current} brand={brand} />}
      {section === "dead" && <DeadPages ws={ws} site={current} canManage={canManage} brand={brand} go={go} />}
      {section === "top" && <TopPages ws={ws} site={current} canManage={canManage} />}
      {section === "settings" && <Settings ws={ws} canManage={canManage} brand={brand} go={go} />}
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
  const [open, setOpen] = useState<LinkProspect | null>(null);

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
  const checkOne = async (p: LinkProspect) => {
    const { error } = await api.POST("/v1/workspaces/{ws}/link-prospects/{prospect}/check", { params: { path: { ws, prospect: p.id } } });
    if (error) return setError(errorMessage(error));
    toast(`Checking ${p.domain}. It takes a minute; checks run at most once an hour.`);
  };
  const checkAll = async () => {
    setBusy("check");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/link-prospects/check", { params: { path: { ws } }, body: { site_id: site.id } });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    const n = (data as { count?: number }).count ?? 0;
    toast(n ? `Checking ${n} page${n === 1 ? "" : "s"}. Results show here in a few minutes.` : "Every page to pitch was checked already.");
  };
  const reclaim = async () => {
    setBusy("reclaim");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/link-prospects/reclaim", { params: { path: { ws } }, body: { site_id: site.id } });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    toast(data.added ? `Added ${data.added} lost link${data.added === 1 ? "" : "s"} to win back. Check their pages next.` : "No new lost links in the last 90 days.");
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
        {p.stage === "prospect" && <Button variant="ghost" disabled={busy === p.id} onClick={() => setOpen(p)}>Pitch</Button>}
        <Menu
          aria={`More for ${p.page_url}`}
          items={[
            { label: "Details", onClick: () => setOpen(p) },
            { label: p.check ? "Check the page again" : "Check the page", onClick: () => checkOne(p) },
            ...(moves[p.stage] ?? []).map((s) => ({ label: moveLabel[s], onClick: () => move(p, s) })),
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
          <span className="flex-1" />
          <Button variant="ghost" disabled={busy === "reclaim"} onClick={reclaim}>Win back lost links</Button>
          <Button variant="ghost" disabled={busy === "check"} onClick={checkAll}>Check pages</Button>
        </form>
      )}
      {open && <ProspectSheet ws={ws} prospect={open} canManage={canManage} onClose={() => setOpen(null)} onChanged={() => { setOpen(null); setReload((n) => n + 1); }} />}
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
              {p.check && <div className="text-xs text-muted-foreground">{checkSummary(p.check)}</div>}
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
                  {p.check && (
                    <button className="block max-w-72 truncate text-left text-[12px] text-muted-foreground underline-offset-2 hover:underline" onClick={() => setOpen(p)}>
                      {checkSummary(p.check)}{p.contacts.length > 0 && ` · ${p.contacts.length} contact${p.contacts.length === 1 ? "" : "s"}`}
                    </button>
                  )}
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
                  {p.stage === "agreed" && p.overdue_at && <div className="text-[11px] text-amber-600 dark:text-amber-400">Not live yet: nudge them</div>}
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

function ProspectSheet({ ws, prospect: p, canManage, onClose, onChanged }: { ws: string; prospect: LinkProspect; canManage: boolean; onClose: () => void; onChanged: () => void }) {
  const [error, setError] = useState("");
  const c = p.check;
  const offer = async (url: string) => {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/link-prospects/{prospect}", { params: { path: { ws, prospect: p.id } }, body: { target_url: url } });
    if (error) return setError(errorMessage(error));
    onChanged();
  };
  const check = async () => {
    const { error } = await api.POST("/v1/workspaces/{ws}/link-prospects/{prospect}/check", { params: { path: { ws, prospect: p.id } } });
    if (error) return setError(errorMessage(error));
    toast(`Checking ${p.domain}. It takes a minute; checks run at most once an hour.`);
  };
  const row = (label: string, value: React.ReactNode) => (
    <div className="grid grid-cols-[8rem_1fr] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{value}</dd>
    </div>
  );
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{p.domain}</SheetTitle>
          <SheetDescription className="break-all">{p.page_url}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          {error && <ErrorText>{error}</ErrorText>}
          <dl className="space-y-1.5">
            {row("Stage", <Badge tone={stageTone[p.stage]}>{stageLabel[p.stage]}</Badge>)}
            {row("Score", `${p.score} (${p.score_factors.map((f) => `${f.code.replace(/_/g, " ")} ${f.points > 0 ? "+" : ""}${f.points}`).join(", ")})`)}
            {row("Found by", methodLabel[p.method])}
            {row("Your page to offer", p.target_url ? <a className="underline-offset-2 hover:underline" href={p.target_url} target="_blank" rel="noreferrer noopener">{path(p.target_url)}</a> : "Not chosen")}
          </dl>
          {!c ? (
            <p className="text-muted-foreground">Not checked yet. OutflareBot reads the page, its outbound links and contact pages, and your AI reads what it's about.</p>
          ) : (
            <>
              <section className="space-y-1.5">
                <h3 className="font-medium">The page, {ago(c.checked_at)}</h3>
                <dl className="space-y-1.5">
                  {row("Status", checkLabel[c.status])}
                  {c.title && row("Title", c.title)}
                  {c.author && row("Author", c.author)}
                  {c.page_kind && row("Kind", c.page_kind.replace(/_/g, " "))}
                  {c.topic && row("Topic", c.topic)}
                  {c.fit != null && row("Fit", `${c.fit} of 100${c.fit_reason ? `: ${c.fit_reason}` : ""}`)}
                  {c.ai_error === "no_ai" && row("AI", "Connect your AI model to have it read pages.")}
                  {row("Links to you", c.links_to_us ? c.our_links.map((l) => `${path(l.url)}${l.nofollow ? " (nofollow)" : ""}`).join(", ") : "No")}
                </dl>
                {c.suggested_target && c.suggested_target !== p.target_url && (
                  <div className="flex flex-wrap items-center gap-2 rounded-md border px-3 py-2">
                    <span className="min-w-0 flex-1">Suggested page to offer: <span className="font-mono text-[12px]">{path(c.suggested_target)}</span>{c.suggested_reason && <span className="block text-[12px] text-muted-foreground">{c.suggested_reason}</span>}</span>
                    {canManage && <Button variant="ghost" onClick={() => offer(c.suggested_target)}>Use it</Button>}
                  </div>
                )}
              </section>
              <section className="space-y-1.5">
                <h3 className="font-medium">Broken links ({c.broken.length} of {c.outbound} outbound)</h3>
                {c.broken.length === 0 ? (
                  <p className="text-muted-foreground">None found.</p>
                ) : (
                  <ul className="space-y-1">
                    {c.broken.map((b) => (
                      <li key={b.url} className="break-all">
                        <span className="font-mono text-[12px]">{b.url}</span>
                        <span className="text-[12px] text-muted-foreground"> · {b.status || "no answer"}{b.anchor && ` · “${b.anchor}”`}</span>
                        {b.archive_url && (
                          <a className="block text-[12px] underline-offset-2 hover:underline" href={b.archive_url} target="_blank" rel="noreferrer noopener nofollow">
                            What it said ({b.archived_at ? new Date(b.archived_at).toLocaleDateString() : "archived"})
                          </a>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            </>
          )}
          <section className="space-y-1.5">
            <h3 className="font-medium">Who to contact</h3>
            {p.contacts.length === 0 ? (
              <p className="text-muted-foreground">Nobody found yet.</p>
            ) : (
              <ul className="space-y-1">
                {p.contacts.map((k) => (
                  <li key={k.record_id}>
                    {k.email ? <a className="underline-offset-2 hover:underline" href={`mailto:${k.email}`}>{k.email}</a> : k.name}
                    <span className="text-[12px] text-muted-foreground"> · {sourceLabel[k.source]} · {k.confidence}% sure</span>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <PitchSection ws={ws} prospect={p} canManage={canManage} onChanged={onChanged} />
          {canManage && <Button variant="ghost" onClick={check}>{c ? "Check again" : "Check the page"}</Button>}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// PitchSection starts a pitch of a prospect to pitch, or shows its latest.
function PitchSection({ ws, prospect: p, canManage, onChanged }: { ws: string; prospect: LinkProspect; canManage: boolean; onChanged: () => void }) {
  const [pitches, setPitches] = useState<LinkPitch[] | null>(null);
  const [seqs, setSeqs] = useState<LinkSequence[] | null>(null);
  const [seq, setSeq] = useState("");
  const emailed = p.contacts.filter((k) => k.email);
  const [contact, setContact] = useState(emailed[0]?.record_id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/link-pitches", { params: { path: { ws }, query: { prospect_id: p.id, limit: 5 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setPitches(data.data);
    });
    if (canManage && p.stage === "prospect")
      api.GET("/v1/workspaces/{ws}/link-building/sequences", { params: { path: { ws } } }).then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setSeqs(data.data);
        setSeq(data.data.find((x) => x.uses_opener)?.sequence_id ?? data.data[0]?.sequence_id ?? "");
      });
  }, [ws, p.id, p.stage, canManage]);
  const start = async () => {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/link-prospects/{prospect}/pitches", {
      params: { path: { ws, prospect: p.id } },
      body: { sequence_id: seq, ...(contact ? { record_id: contact } : {}) },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    const s = seqs?.find((x) => x.sequence_id === seq);
    toast(s?.approve_pitches === false ? "Writing the opener. It goes out once written." : "Writing the opener. Approve it under Pitches.");
    onChanged();
  };
  const latest = pitches?.[0];
  const open = latest && (latest.status === "writing" || latest.status === "draft");
  const chosen = seqs?.find((x) => x.sequence_id === seq);
  return (
    <section className="space-y-1.5">
      <h3 className="font-medium">Pitch</h3>
      {error && <ErrorText>{error}</ErrorText>}
      {latest && (
        <p>
          <Badge tone={pitchTone[latest.status]}>{pitchLabel[latest.status]}</Badge>{" "}
          to {latest.contact_email ?? latest.contact_name} through {latest.sequence_name}
          {latest.reply_kind && <span className="text-muted-foreground"> · {replyLabel[latest.reply_kind]}</span>}
        </p>
      )}
      {canManage && p.stage === "prospect" && !open && (
        emailed.length === 0 ? (
          <p className="text-muted-foreground">No contact with an email address yet. Check the page to find who to write to.</p>
        ) : !seqs ? (
          <Loading />
        ) : seqs.length === 0 ? (
          <p className="text-muted-foreground">Make a sequence for link pitches first, with {"{{custom.opener}}"} in its first step.</p>
        ) : (
          <div className="space-y-2">
            <div className="flex flex-wrap gap-2">
              <select className={field} aria-label="Contact" value={contact} onChange={(e) => setContact(e.target.value)}>
                {emailed.map((k) => <option key={k.record_id} value={k.record_id}>{k.email}</option>)}
              </select>
              <select className={field} aria-label="Sequence" value={seq} onChange={(e) => setSeq(e.target.value)}>
                {seqs.map((x) => <option key={x.sequence_id} value={x.sequence_id}>{x.name}</option>)}
              </select>
            </div>
            {chosen && !chosen.uses_opener && <p className="text-[12px] text-amber-600 dark:text-amber-400">This sequence doesn't use {"{{custom.opener}}"}, so the AI's opener won't be in the email.</p>}
            <p className="text-[12px] text-muted-foreground">Your AI writes an opener for this page{chosen?.approve_pitches === false ? "; it goes out as soon as it's written." : " for you to approve."} It never offers payment for a link.</p>
            <Button disabled={busy || !seq} onClick={start}>Write a pitch</Button>
          </div>
        )
      )}
    </section>
  );
}

function Pitches({ ws, canManage, brand }: { ws: string; canManage: boolean; brand: { brand?: string } }) {
  const [status, setStatus] = useState<PitchStatus | "">("draft");
  const [list, setList] = useState<LinkPitch[] | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    setList(null);
    api.GET("/v1/workspaces/{ws}/link-pitches", { params: { path: { ws }, query: { ...brand, ...(status ? { status } : {}), limit: 200 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, status, reload, brand.brand]);
  const approve = async (k: LinkPitch) => {
    const opener = (edits[k.id] ?? k.opener).trim();
    if (!opener) return setError("Write an opener first.");
    if (!(await confirmAction(`Send the pitch to ${k.contact_email ?? k.contact_name}?`, `They are enrolled in ${k.sequence_name}, whose sending limits and checks apply.`, "Send"))) return;
    setBusy(k.id);
    const { error } = await api.POST("/v1/workspaces/{ws}/link-pitches/{pitch}/approve", { params: { path: { ws, pitch: k.id } }, body: { opener } });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    toast(`Pitch to ${k.domain} approved.`);
    setReload((n) => n + 1);
  };
  const reject = async (k: LinkPitch) => {
    setBusy(k.id);
    const { error } = await api.POST("/v1/workspaces/{ws}/link-pitches/{pitch}/reject", { params: { path: { ws, pitch: k.id } }, body: {} });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      <select className={field} aria-label="Show" value={status} onChange={(e) => setStatus(e.target.value as PitchStatus | "")}>
        <option value="draft">To approve</option>
        <option value="enrolled">Sent</option>
        <option value="writing">Being written</option>
        <option value="rejected">Rejected</option>
        <option value="">All</option>
      </select>
      {!list ? (
        <Loading />
      ) : list.length === 0 ? (
        <Empty title={status === "draft" ? "Nothing to approve" : "No pitches"}>Pitch a prospect from its details under Prospects.</Empty>
      ) : (
        <ul className="space-y-3">
          {list.map((k) => (
            <li key={k.id} className="space-y-2 rounded-md border p-3 text-[13px]">
              <div className="flex flex-wrap items-center gap-2">
                <a className="font-medium underline-offset-2 hover:underline" href={k.page_url} target="_blank" rel="noreferrer noopener nofollow">{k.domain}</a>
                <Badge tone={pitchTone[k.status]}>{pitchLabel[k.status]}</Badge>
                {k.reply_kind && <Badge tone={k.reply_kind === "agreed" ? "green" : k.reply_kind === "declined" ? "grey" : "amber"}>{replyLabel[k.reply_kind]}</Badge>}
                <span className="text-[12px] text-muted-foreground">to {k.contact_email ?? k.contact_name} · {k.sequence_name} · {ago(k.updated_at)}</span>
              </div>
              <div className="font-mono text-[12px] break-all text-muted-foreground">{path(k.page_url)}{k.target_url && ` → offer ${path(k.target_url)}`}</div>
              {k.reason && draftReason[k.reason] && <p className="text-[12px] text-muted-foreground">{draftReason[k.reason]}</p>}
              {k.status === "draft" && canManage ? (
                <>
                  <textarea
                    className={`${field} h-auto min-h-20 w-full py-2`}
                    aria-label={`Opener for ${k.domain}`}
                    maxLength={2000}
                    value={edits[k.id] ?? k.opener}
                    onChange={(e) => setEdits({ ...edits, [k.id]: e.target.value })}
                  />
                  <div className="flex gap-2">
                    <Button disabled={busy === k.id} onClick={() => approve(k)}>Approve and send</Button>
                    <Button variant="ghost" disabled={busy === k.id} onClick={() => reject(k)}>Reject</Button>
                  </div>
                </>
              ) : (
                k.opener && <p className="whitespace-pre-wrap">{k.opener}</p>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Results({ ws, site, brand }: { ws: string; site: SEOSite; brand: { brand?: string } }) {
  const [days, setDays] = useState(90);
  const [res, setRes] = useState<{ totals: LinkResultRow; data: LinkResultRow[] } | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setRes(null);
    api.GET("/v1/workspaces/{ws}/link-prospects/results", { params: { path: { ws }, query: { ...brand, site_id: site.id, days } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRes(data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, site.id, days, brand.brand]);
  const pct = (v: number | null) => (v == null ? "–" : `${Math.round(v * 100)}%`);
  const label = (m: string) => (m === "all" ? "All methods" : (methodLabel[m as Method] ?? m));
  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      <div className="flex flex-wrap items-center gap-2">
        <select className={field} aria-label="Period" value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {[30, 90, 180, 365].map((d) => <option key={d} value={d}>Found in the last {d} days</option>)}
        </select>
        <span className="text-[12px] text-muted-foreground">Cost per live link is in the Outcomes report, by sequence.</span>
      </div>
      {!res ? (
        <Loading />
      ) : (
        <DataTable
          data={[...res.data, res.totals]}
          rowId={(r) => r.method}
          empty="No prospects found in this period."
          columns={[
            { id: "method", header: "Found by", accessorFn: (r) => r.method, cell: ({ row }) => <span className={row.original.method === "all" ? "font-medium" : ""}>{label(row.original.method)}</span> },
            ...(["found", "pitched", "replied", "agreed", "live", "declined", "removed"] as const).map((k) => ({
              id: k, header: k[0].toUpperCase() + k.slice(1), accessorFn: (r: LinkResultRow) => r[k], meta: { align: "right" as const, mono: true },
            })),
            { id: "win", header: "Live per pitch", accessorFn: (r) => r.win_rate ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.win_rate) },
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

function Settings({ ws, canManage, brand, go }: { ws: string; canManage: boolean; brand: { brand?: string }; go: (tab: Tab, item?: string) => void }) {
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
      <PitchSequences ws={ws} canManage={canManage} brand={brand} go={go} />
    </form>
  );
}

// PitchSequences turns approval of each sequence's link pitches on or off.
function PitchSequences({ ws, canManage, brand, go }: { ws: string; canManage: boolean; brand: { brand?: string }; go: (tab: Tab, item?: string) => void }) {
  const [list, setList] = useState<LinkSequence[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/link-building/sequences", { params: { path: { ws }, query: brand } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand.brand]);
  const toggle = async (q: LinkSequence, on: boolean) => {
    if (!on && !(await confirmAction(`Send ${q.name}'s pitches without approval?`, "Each pitch goes out as soon as your AI writes its opener, without a person reading it first.", "Send without approval"))) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/link-building/sequences/{sequence}", { params: { path: { ws, sequence: q.sequence_id } }, body: { approve_pitches: on } });
    if (error) return setError(errorMessage(error));
    setError("");
    setList((l) => l?.map((x) => (x.sequence_id === q.sequence_id ? { ...x, approve_pitches: on } : x)) ?? null);
  };
  return (
    <section className="space-y-2 border-t pt-4">
      <h3 className="text-[13px] font-medium">Approving pitches</h3>
      <p className="text-[12px] text-muted-foreground">Pitches go out through your sequences, with your AI's opener as {"{{custom.opener}}"} in the first step. Each waits for approval unless you turn that off for its sequence.</p>
      {error && <ErrorText>{error}</ErrorText>}
      {!list ? (
        <Loading />
      ) : list.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">No sequences yet.</p>
      ) : (
        <ul className="space-y-1.5">
          {list.map((q) => (
            <li key={q.sequence_id} className="flex flex-wrap items-center gap-2 text-[13px]">
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={q.approve_pitches} disabled={!canManage} onChange={(e) => toggle(q, e.target.checked)} />
                <span>Approve each pitch through</span>
              </label>
              <button type="button" className="underline-offset-2 hover:underline" onClick={() => go("sequences", q.sequence_id)}>{q.name}</button>
              {!q.uses_opener && <span className="text-[12px] text-muted-foreground">(no {"{{custom.opener}}"} in its steps)</span>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
