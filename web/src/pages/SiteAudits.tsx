import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Crawl, CrawlIssue, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { ConnectFor, geonodeWhy } from "@/components/connect-for";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago, plural } from "../format";
import { track } from "@/lib/analytics";
import { dnsHelp, DNSWays, heldByMission, takeDNSFocus } from "@/components/dns-one-click";
import { takeConnectOutcome } from "./Connectors";
import type { Tab } from "../App";
import { Indexability } from "./Indexability";

// Site audits: the websites OutflareBot crawls, each with its ownership
// proof and its latest audit (health score, issues by URL template). The
// issues become fixes on the workspace's fixes list.

type Tone = keyof typeof tones;
type Props = { ws: string; canManage: boolean; open: string; onOpen: (site: string) => void; go: (tab: Tab, item?: string) => void };

export const severityTone: Record<string, Tone> = { error: "red", warning: "amber", notice: "grey" };
export const severityLabel: Record<string, string> = { error: "Error", warning: "Warning", notice: "Notice" };

const running = (c?: Crawl | null) => c?.status === "queued" || c?.status === "running";

export function scoreTone(score?: number | null): Tone {
  if (score === null || score === undefined) return "grey";
  return score >= 90 ? "green" : score >= 70 ? "amber" : "red";
}

const reasons: Record<string, string> = {
  done: "Read every page it found.",
  limit: "Stopped at the site's page limit.",
  stopped: "Stopped by hand.",
  opted_out: "The site has opted out of OutflareBot.",
  robots_unreachable: "robots.txt did not answer, so no page may be read.",
  site_errors: "Ten errors in a row; the site may be down.",
  error: "Something broke on our side.",
};

function crawlLine(c: Crawl) {
  if (running(c)) return `Crawling · ${plural(c.pages_fetched, "page")} read of ${c.pages_found.toLocaleString()} found`;
  const end = c.finished_at ? ago(c.finished_at) : "";
  return `${plural(c.pages_fetched, "page")} read · ${end}`;
}

export function SiteAudits(props: Props) {
  // Back from signing in to Cloudflare for a site's proof: say how it went.
  useEffect(() => {
    takeConnectOutcome();
    takeDNSFocus();
  }, []);
  return props.open ? <Site key={props.open} {...props} /> : <Sites {...props} />;
}

// ---- All sites ----

function Sites({ ws, canManage, onOpen, go }: Props) {
  const [sites, setSites] = useState<SEOSite[] | null>(null);
  const [error, setError] = useState("");
  const brand = useBrand(ws).query;
  const brandKey = brand.brand ?? "";

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brandKey ? { brand: brandKey } : {} } });
    if (error || !data) return setError(errorMessage(error));
    setSites(data.data);
  }, [ws, brandKey]);
  useEffect(() => {
    load();
  }, [load]);
  const busy = sites?.some((s) => running(s.last_crawl));
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [busy, load]);

  const header = (
    <PageHeader
      title="Site audits"
      description="OutflareBot crawls your websites politely, checks every page against 61 rules and gives each crawl a health score. What it finds goes on the fixes list."
      action={sites && sites.length > 0 && <Button variant="ghost" onClick={() => go("fixes")}>Open fixes</Button>}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sites) return <div className="space-y-6">{header}<Loading /></div>;

  const audited = sites.filter((s) => s.last_crawl?.health_score !== null && s.last_crawl?.health_score !== undefined);
  const avg = audited.length ? Math.round(audited.reduce((n, s) => n + (s.last_crawl!.health_score ?? 0), 0) / audited.length) : null;
  const errors = sites.reduce((n, s) => n + (s.last_crawl?.issues.errors ?? 0), 0);

  return (
    <div className="space-y-6">
      {header}
      {sites.length > 0 && (
        <Stats cols={4}>
          <Stat label="Sites" value={sites.length} />
          <Stat label="Proved yours" value={sites.filter((s) => s.verified).length} />
          <Stat label="Average health" value={avg === null ? "–" : avg} tone={avg === null ? undefined : avg >= 90 ? "green" : avg >= 70 ? "amber" : "red"} />
          <Stat label="Errors in latest crawls" value={errors.toLocaleString()} tone={errors > 0 ? "red" : undefined} />
        </Stats>
      )}
      {sites.length === 0 && (
        <Empty title="Audit your first site">
          Add your website's address. Outflare reads up to 100 pages right away; prove you own it to crawl up to 10,000 pages, faster.{" "}
          {!canManage && "Ask someone who can manage SEO to add one."}
        </Empty>
      )}
      {sites.length > 0 && (
        <DataTable
          data={sites}
          rowId={(s) => s.id}
          onRowClick={(s) => onOpen(s.id)}
          searchPlaceholder="Search sites"
          initialSort={[{ id: "health", desc: false }]}
          mobile={(s) => (
            <div className="space-y-1.5">
              <div className="flex items-start justify-between gap-2">
                <span className="flex flex-wrap items-center gap-1.5"><span className="font-mono font-medium break-all">{s.host}</span> <BrandBadge id={s.brand_id} /></span>
                <Health crawl={s.last_crawl} />
              </div>
              <div className="text-xs text-muted-foreground">{s.verified ? "Proved yours" : "Not proved"} · {s.last_crawl ? crawlLine(s.last_crawl) : "Not crawled yet"}</div>
            </div>
          )}
          columns={[
            {
              id: "host",
              header: "Site",
              accessorFn: (s) => s.host,
              enableHiding: false,
              cell: ({ row: { original: s } }) => (
                <>
                  <span className="font-mono text-[13px] font-medium group-hover:underline">{s.host}</span> <BrandBadge id={s.brand_id} />
                  <div className="text-xs text-muted-foreground">{s.schedule === "off" ? "Crawled by hand" : `Crawled ${s.schedule}`}{s.status === "paused" && " · paused"}</div>
                </>
              ),
            },
            { id: "verified", header: "Ownership", accessorFn: (s) => (s.verified ? 1 : 0), cell: ({ row: { original: s } }) => (s.verified ? <Badge tone="green">✓ Proved</Badge> : <Badge>Not proved</Badge>) },
            { id: "health", header: "Health", accessorFn: (s) => s.last_crawl?.health_score ?? 101, cell: ({ row: { original: s } }) => <Health crawl={s.last_crawl} /> },
            {
              id: "issues",
              header: "Issues",
              accessorFn: (s) => s.last_crawl?.issues.errors ?? 0,
              cell: ({ row: { original: s } }) => (s.last_crawl?.health_score === null || !s.last_crawl ? <span className="text-muted-foreground">–</span> : <IssueCounts crawl={s.last_crawl} />),
            },
            { id: "crawl", header: "Latest crawl", accessorFn: (s) => s.last_crawl?.finished_at ?? "", cell: ({ row: { original: s } }) => <span className="text-[13px] text-muted-foreground">{s.last_crawl ? crawlLine(s.last_crawl) : "Not crawled yet"}</span> },
          ]}
        />
      )}
      {canManage && <AddSite ws={ws} taken={sites.map((s) => s.host)} onAdded={onOpen} />}
    </div>
  );
}

function Health({ crawl }: { crawl?: Crawl | null }) {
  if (!crawl) return <Badge>Not crawled</Badge>;
  if (running(crawl)) return <Badge tone="blue">Crawling…</Badge>;
  if (crawl.health_score === null) return <Badge title={crawl.reason ? reasons[crawl.reason] : undefined}>No score</Badge>;
  return <Badge tone={scoreTone(crawl.health_score)} title="The share of pages with no error">{crawl.health_score} / 100</Badge>;
}

function IssueCounts({ crawl }: { crawl: Crawl }) {
  const { errors, warnings, notices } = crawl.issues;
  if (errors + warnings + notices === 0) return <span className="text-[13px] text-green-700 dark:text-green-400">None</span>;
  return (
    <span className="inline-flex flex-wrap gap-1">
      {errors > 0 && <Badge tone="red">{plural(errors, "error")}</Badge>}
      {warnings > 0 && <Badge tone="amber">{plural(warnings, "warning")}</Badge>}
      {notices > 0 && <Badge>{plural(notices, "notice")}</Badge>}
    </span>
  );
}

const bare = (h: string) => h.toLowerCase().replace(/^https?:\/\//, "").replace(/\/.*$/, "").replace(/^www\./, "");

// proofNames says how a site was proved, in words.
const proofNames: Record<string, string> = {
  search_console: "your Search Console connection",
  tracking: "the Outflare tracking tag on its home page",
  dns: "a DNS record",
  meta: "a meta tag",
  file: "a file",
  test: "test mode",
};

function AddSite({ ws, taken, onAdded }: { ws: string; taken: string[]; onAdded: (id: string) => void }) {
  const brand = useBrand(ws).brand;
  // The selected brand's first website not audited yet, filled in for you.
  const suggested = brand ? [brand.website, ...brand.domains].map((d) => d?.trim()).find((d) => d && !taken.some((t) => bare(t) === bare(d))) ?? "" : "";
  const [url, setUrl] = useState(suggested);
  const [schedule, setSchedule] = useState<"off" | "daily" | "weekly">("weekly");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => setUrl(suggested), [suggested]);

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const address = /^https?:\/\//i.test(url.trim()) ? url.trim() : `https://${url.trim()}`;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws } }, body: { url: address, schedule, ...(brand ? { brand_id: brand.id } : {}) } });
    if (error || !data) {
      setBusy(false);
      return setError(errorMessage(error));
    }
    const path = { params: { path: { ws, site: data.id } } };
    // Look for a proof you already have (Search Console, the tracking tag)
    // before the first crawl, so it runs with the proved limits.
    const proof = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/verify", path);
    const proved = proof.data && "verified" in proof.data && proof.data.verified ? proof.data.verify_method : null;
    const crawl = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/crawls", path);
    setBusy(false);
    if (crawl.error) toast(errorMessage(crawl.error), "error");
    else if (proved) toast(`Added ${data.host}, proved yours through ${proofNames[proved] ?? proved}. The first crawl has started.`);
    else toast(`Added ${data.host}. The first crawl has started; prove it's yours to crawl every page.`);
    onAdded(data.id);
  }

  const forBrand = brand && suggested && url === suggested;
  return (
    <Card
      title={forBrand ? `Audit ${brand.name}'s website` : "Add a site"}
      description={forBrand ? `We filled in ${brand.name}'s website. Change the address if the site to audit is somewhere else.` : "Its pages are read as OutflareBot, which obeys robots.txt and slows down when the site struggles."}
    >
      <form onSubmit={add} className="flex flex-wrap items-end gap-2">
        <label className="min-w-64 flex-1 space-y-1 text-[13px]">
          <span className="font-medium">Address</span>
          <span className="relative block">
            <input className={`${field} w-full pr-8`} placeholder="https://www.example.com" value={url} onChange={(e) => setUrl(e.target.value)} required />
            {url && (
              <button type="button" aria-label="Clear the address" className="absolute top-1/2 right-2 -translate-y-1/2 text-muted-foreground hover:text-foreground" onClick={() => setUrl("")}>
                ×
              </button>
            )}
          </span>
        </label>
        <label className="space-y-1 text-[13px]">
          <span className="font-medium">Crawl</span>
          <select className={`${field} w-full`} value={schedule} onChange={(e) => setSchedule(e.target.value as typeof schedule)}>
            <option value="weekly">Every week</option>
            <option value="daily">Every day</option>
            <option value="off">Only by hand</option>
          </select>
        </label>
        <Button type="submit" disabled={busy || !url.trim()}>{busy ? "Adding…" : "Add and crawl"}</Button>
      </form>
      <p className="mt-2 text-[12px] text-muted-foreground">
        Outflare checks right away whether you've already proved it's yours: a Search Console connection that owns the site, or the Outflare tracking tag on its home page.
      </p>
      <ErrorText>{error}</ErrorText>
    </Card>
  );
}

// ---- One site ----

function Site({ ws, canManage, open, onOpen, go }: Props) {
  const [site, setSite] = useState<SEOSite | null>(null);
  const [missing, setMissing] = useState(false);
  const [crawls, setCrawls] = useState<Crawl[]>([]);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(false);

  const load = useCallback(async () => {
    const [s, c] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/seo-sites/{site}", { params: { path: { ws, site: open } } }),
      api.GET("/v1/workspaces/{ws}/crawls", { params: { path: { ws }, query: { site: open, limit: 10 } } }),
    ]);
    if (s.response.status === 404) return setMissing(true);
    if (s.error || !s.data) return setError(errorMessage(s.error));
    setSite(s.data);
    setCrawls(c.data?.data ?? []);
  }, [ws, open]);
  useEffect(() => {
    load();
  }, [load]);
  // The breadcrumb names the site rather than its ID.
  useEffect(() => {
    if (site) window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id: site.id, name: site.host } }));
  }, [site?.id, site?.host]); // eslint-disable-line react-hooks/exhaustive-deps
  // While a crawl runs or the DNS proof is being added and looked for.
  const dnsAt = site?.dns_install && !site.verified ? Date.parse(site.dns_install.at) : 0;
  const busy = running(site?.last_crawl) || site?.dns_install?.status === "queued" || Date.now() - dnsAt < 15 * 60_000;
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [busy, load]);

  const back = { label: "All sites", onClick: () => onOpen("") };
  if (missing) {
    return (
      <div>
        <PageHeader title="Site not found" back={back} />
        <Empty title="Site not found">It may have been removed, or it belongs to a brand you can't see.</Empty>
      </div>
    );
  }
  if (error && !site) return <div><PageHeader title="Site" back={back} /><ErrorText>{error}</ErrorText></div>;
  if (!site) return <Loading />;

  const path = { params: { path: { ws, site: site.id } } };
  const proofRecord = [{ type: "TXT", name: site.verification.dns_name, value: site.verification.dns_value }];
  const last = site.last_crawl;

  async function crawl() {
    const { error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/crawls", path);
    if (error) return toast(errorMessage(error), "error");
    toast(`Crawling ${site!.host}.`);
    load();
  }
  async function stop() {
    if (!last) return;
    const { error } = await api.POST("/v1/workspaces/{ws}/crawls/{crawl}/stop", { params: { path: { ws, crawl: last.id } } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }
  async function verify() {
    setChecking(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/verify", path);
    setChecking(false);
    if (error) return toast(errorMessage(error), "error");
    if (data && "verified" in data && data.verified) toast(`${site!.host} is proved yours, through ${proofNames[data.verify_method ?? ""] ?? "its proof"}. Crawls can now read up to ${(10000).toLocaleString()} pages.`);
    else toast("No proof found yet. DNS changes can take a few minutes to show.", "error");
    load();
  }
  async function patch(body: Record<string, unknown>) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-sites/{site}", { ...path, body });
    if (error) return toast(errorMessage(error), "error");
    load();
  }
  async function remove() {
    if (!(await confirmAction(`Stop auditing ${site!.host}?`, <p>Its crawls, issues and fixes are deleted. Add it again any time.</p>, "Remove site"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/seo-sites/{site}", path);
    if (error) return toast(errorMessage(error), "error");
    toast(`Removed ${site!.host}.`);
    onOpen("");
  }

  return (
    <div className="space-y-6">
      <PageHeader
        back={back}
        title={<><span className="break-all font-mono">{site.host}</span>{site.verified ? <Badge tone="green" title={site.verify_method ? `Proved through ${proofNames[site.verify_method] ?? site.verify_method}` : undefined}>✓ Proved yours</Badge> : <Badge>Not proved</Badge>}<BrandBadge id={site.brand_id} /></>}
        description={<>Starts at <span className="font-mono">{site.start_url}</span> · up to {plural(site.max_pages, "page")} · {site.schedule === "off" ? "crawled by hand" : `crawled ${site.schedule}`}</>}
        action={canManage && (busy ? <Button variant="ghost" onClick={stop}>Stop crawl</Button> : <Button onClick={crawl}>Crawl now</Button>)}
      />

      {last && <LatestCrawl ws={ws} site={open} crawl={last} siteVerified={site.verified} go={go} />}
      {last && <Indexability ws={ws} site={open} go={go} />}
      {!last && <Empty title="Not crawled yet">{canManage ? "Crawl now to get its first audit." : "Its first crawl will show here."}</Empty>}

      {!site.verified && (
        <Card title="Prove you own it" description={`Proved sites are crawled up to ${(10000).toLocaleString()} pages, faster, may audit pages their own robots.txt hides, and can read pages a firewall blocks through your Geonode connection.`}>
          <ol className="space-y-4 text-[13px]">
            <li>
              <div className="font-medium">1. Use what you already have</div>
              <p className="text-muted-foreground">
                Outflare looks for either of these, with nothing to add: a{" "}
                <button className="underline hover:text-foreground" onClick={() => go("connectors")}>Google Search Console connection</button> that owns {site.host}, or the{" "}
                <button className="underline hover:text-foreground" onClick={() => go("tracking")}>Outflare tracking tag</button> on its home page.
              </p>
              {canManage && <div className="mt-2"><Button onClick={verify} disabled={checking}>{checking ? "Checking…" : "Check now"}</Button></div>}
            </li>
            <li>
              <div className="mb-2 font-medium">2. Or add a DNS record</div>
              <DNSWays ws={ws} page="audits" item={site.id} approval={{ kind: "seo_site", id: site.id }} connections={site.dns_connections} canManage={canManage} reload={load}
                records={proofRecord} run={site.dns_install} {...dnsHelp(site.host, proofRecord, "so Outflare can confirm the site is ours")}
                used={(way, how) => track("ui.dns_one_click_used", { where: "seo_site", way, how })}
                manual={<>Add the record below where {site.host} is managed, then press Check now.</>}
                intro={<>Quickest: connect the Cloudflare account {site.host} is on, and Outflare adds the record for you.</>}
                after="Then Outflare looks for it every minute until the site is proved."
                onAdd={async (integration) => {
                  const { data, error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/dns", { ...path, body: { integration_id: integration } });
                  return { error, held: heldByMission(data) };
                }} />
            </li>
            <li>
              <details>
                <summary className="cursor-pointer font-medium">3. Or add a proof by hand</summary>
                <ul className="mt-2 space-y-3">
                  <li>
                    <div className="font-medium">A DNS record</div>
                    <div className="text-muted-foreground">A TXT record on <span className="font-mono">{site.verification.dns_name}</span>:</div>
                    <Code text={site.verification.dns_value} />
                  </li>
                  <li>
                    <div className="font-medium">A meta tag</div>
                    <div className="text-muted-foreground">In the head of the start page:</div>
                    <Code text={site.verification.meta_tag} />
                  </li>
                  <li>
                    <div className="font-medium">A file</div>
                    <div className="text-muted-foreground">At <span className="font-mono break-all">{site.verification.file_url}</span>, holding:</div>
                    <Code text={site.verification.file_body} />
                  </li>
                </ul>
                {canManage && <div className="mt-3"><Button variant="ghost" onClick={verify} disabled={checking}>{checking ? "Checking…" : "Check now"}</Button></div>}
              </details>
            </li>
          </ol>
        </Card>
      )}

      {crawls.length > 1 && (
        <Card title="Crawl history">
          <ul className="divide-y text-[13px]">
            {crawls.map((c) => (
              <li key={c.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                <span>{c.finished_at ? ago(c.finished_at) : running(c) ? "Running now" : ago(c.created_at)} · {plural(c.pages_fetched, "page")} {c.reason && <span className="text-muted-foreground">· {reasons[c.reason] ?? c.reason}</span>}</span>
                <Health crawl={c} />
              </li>
            ))}
          </ul>
        </Card>
      )}

      {canManage && <CrawlSettings site={site} patch={patch} remove={remove} go={go} />}
    </div>
  );
}

const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const delays = [250, 500, 1000, 2000, 5000, 10000];

function zones(current: string): string[] {
  let all: string[] = [];
  try {
    all = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  } catch {
    all = [];
  }
  return Array.from(new Set(["UTC", current, ...all]));
}

function lines(v: string) {
  return v.split("\n").map((l) => l.trim()).filter(Boolean);
}

// settingsLine says how a crawl ran, in a few words.
function settingsLine(c: Crawl) {
  const s = c.settings;
  const parts = [`up to ${c.max_pages.toLocaleString()} pages`];
  if (s.max_depth > 0) parts.push(`${plural(s.max_depth, "click")} deep`);
  parts.push(s.use_sitemaps ? "links and sitemaps" : "links only");
  if (s.render_js === "always") parts.push("scripts run on every page");
  else if (s.render_js === "off") parts.push("scripts never run");
  if ((s.include_patterns ?? []).length || (s.exclude_patterns ?? []).length) parts.push("own scope rules");
  if (s.ignore_params) parts.push("parameters ignored");
  if (!c.obey_robots) parts.push("robots.txt ignored");
  return parts.join(" · ");
}

function CrawlSettings({ site, patch, remove, go }: { site: SEOSite; patch: (body: Record<string, unknown>) => Promise<void>; remove: () => void; go: (tab: Tab, item?: string) => void }) {
  const tz = site.schedule_tz;
  const label = "font-medium";
  const hint = "block text-[12px] text-muted-foreground";
  const toggle = (key: "use_sitemaps" | "follow_nofollow" | "ignore_params", text: string, sub: string) => (
    <label className="flex items-start gap-2">
      <input type="checkbox" className="mt-0.5" checked={site[key]} onChange={(e) => patch({ [key]: e.target.checked })} />
      <span><span className={label}>{text}</span><span className={hint}>{sub}</span></span>
    </label>
  );
  const patterns = (key: "include_patterns" | "exclude_patterns", text: string, sub: string, example: string) => (
    <label className="flex min-w-64 flex-1 flex-col gap-1">
      <span className={label}>{text}</span>
      <textarea
        key={site[key].join("\n")}
        rows={3}
        className={`${field} w-full font-mono text-[12px]`}
        placeholder={example}
        defaultValue={site[key].join("\n")}
        onBlur={(e) => lines(e.target.value).join("\n") !== site[key].join("\n") && patch({ [key]: lines(e.target.value) })}
      />
      <span className={hint}>{sub}</span>
    </label>
  );
  const renderReady = site.verified && site.render_connections.length > 0;
  return (
    <Card title="Crawl settings" description="Smart defaults read your site the way a search engine does. Changes apply from the next crawl.">
      <div className="space-y-6 text-[13px]">
        <section className="space-y-3">
          <h3 className="text-[13px] font-semibold">What to crawl</h3>
          <div className="flex flex-wrap items-start gap-4">
            <label className="flex min-w-64 flex-1 flex-col gap-1">
              <span className={label}>Start address</span>
              <input key={site.start_url} className={`${field} w-full`} defaultValue={site.start_url} onBlur={(e) => e.target.value.trim() && e.target.value.trim() !== site.start_url && patch({ url: e.target.value.trim() })} />
              <span className={hint}>Where crawls start, on {site.host}</span>
            </label>
            <label className="flex flex-col gap-1">
              <span className={label}>Pages per crawl</span>
              <input key={site.max_pages} className={`${field} w-28`} type="number" min={1} max={site.limits.max_pages} defaultValue={site.max_pages} onBlur={(e) => Number(e.target.value) !== site.max_pages && patch({ max_pages: Number(e.target.value) })} />
              <span className={hint}>At most {site.limits.max_pages.toLocaleString()}{!site.verified && ", or 10,000 once proved"}</span>
            </label>
            <label className="flex flex-col gap-1">
              <span className={label}>How deep</span>
              <select className={`${field} w-full`} value={site.max_depth} onChange={(e) => patch({ max_depth: Number(e.target.value) })}>
                <option value={0}>Every page it finds</option>
                {[1, 2, 3, 5, 10, 16].map((d) => <option key={d} value={d}>{plural(d, "click")} from the start</option>)}
              </select>
            </label>
          </div>
          <div className="grid gap-3 sm:grid-cols-3">
            {toggle("use_sitemaps", "Find pages in sitemaps", "The ones robots.txt lists, or /sitemap.xml.")}
            {toggle("follow_nofollow", "Follow nofollow links", "Off by default, like search engines.")}
            {toggle("ignore_params", "Ignore URL parameters", "Crawl /shoes?color=red as /shoes.")}
          </div>
          <div className="flex flex-wrap gap-4">
            {patterns("include_patterns", "Only crawl pages matching", "One pattern per line, on the path. Empty crawls everything. The start page is always crawled.", "^/blog/")}
            {patterns("exclude_patterns", "Never crawl pages matching", "One pattern per line. Wins over the line above.", "^/tag/\n[?&]sort=")}
          </div>
        </section>

        <section className="space-y-2">
          <h3 className="text-[13px] font-semibold">JavaScript</h3>
          <select className={`${field} w-full max-w-md`} value={site.render_js} onChange={(e) => patch({ render_js: e.target.value })}>
            <option value="auto">Run scripts only on pages that need them (recommended)</option>
            <option value="always">Run scripts on every page</option>
            <option value="off">Never run scripts</option>
          </select>
          <p className={hint}>
            {site.render_js === "off"
              ? "Pages are read exactly as served. Pages built by scripts look empty."
              : renderReady
                ? `Scripts run through your own Geonode connection, which uses your Geonode credits${site.render_js === "always" ? " for every page" : " only for pages that are empty until scripts run"}.`
                : !site.verified
                  ? "Scripts run only on a site you've proved is yours, through your own Geonode account."
                  : "Scripts run through your own Geonode account, which isn't connected yet. Until then pages are read as served."}
          </p>
          {site.render_js !== "off" && site.verified && site.render_connections.length === 0 && (
            <ConnectFor connector="geonode" name="Geonode" back="audits" item={site.id} why={geonodeWhy.audits} go={go} variant="ghost" />
          )}
        </section>

        <section className="space-y-3">
          <h3 className="text-[13px] font-semibold">Speed and robots.txt</h3>
          <div className="flex flex-wrap items-start gap-4">
            <label className="flex max-w-md flex-col gap-1">
              <span className={label}>Pause between pages</span>
              <select className={`${field} w-48`} value={site.crawl_delay_ms} onChange={(e) => patch({ crawl_delay_ms: Number(e.target.value) })}>
                {Array.from(new Set([...delays, site.crawl_delay_ms])).filter((d) => d >= site.limits.min_crawl_delay_ms).sort((a, b) => a - b).map((d) => (
                  <option key={d} value={d}>{d < 1000 ? `${d} ms` : `${d / 1000} second${d === 1000 ? "" : "s"}`}</option>
                ))}
              </select>
              <span className={hint}>{site.verified ? "Slower is gentler on your server." : "At least 2 seconds until the site is proved yours."} The crawl slows down by itself when the site struggles.</span>
            </label>
            {site.limits.may_ignore_robots && (
              <label className="flex items-start gap-2 pt-6">
                <input type="checkbox" className="mt-0.5" checked={!site.obey_robots} onChange={(e) => patch({ obey_robots: !e.target.checked })} />
                <span><span className={label}>Audit pages my robots.txt hides</span><span className={hint}>Only for your own site.</span></span>
              </label>
            )}
          </div>
        </section>

        <section className="space-y-3">
          <h3 className="text-[13px] font-semibold">Schedule</h3>
          <div className="flex flex-wrap items-start gap-4">
            <label className="flex flex-col gap-1">
              <span className={label}>Crawl</span>
              <select className={`${field} w-full`} value={site.schedule} onChange={(e) => patch({ schedule: e.target.value })}>
                <option value="weekly">Every week</option>
                <option value="daily">Every day</option>
                <option value="off">Only by hand</option>
              </select>
            </label>
            {site.schedule === "weekly" && (
              <label className="flex flex-col gap-1">
                <span className={label}>Day</span>
                <select className={`${field} w-full`} value={site.schedule_day ?? ""} onChange={(e) => patch({ schedule_day: e.target.value === "" ? null : Number(e.target.value), ...(site.schedule_hour === null && e.target.value !== "" ? { schedule_hour: 3 } : {}) })}>
                  <option value="">Any day</option>
                  {days.map((d, i) => <option key={d} value={i}>{d}</option>)}
                </select>
              </label>
            )}
            {site.schedule !== "off" && (
              <>
                <label className="flex flex-col gap-1">
                  <span className={label}>Time</span>
                  <select className={`${field} w-full`} value={site.schedule_hour ?? ""} onChange={(e) => patch({ schedule_hour: e.target.value === "" ? null : Number(e.target.value) })}>
                    <option value="">Any time</option>
                    {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{`${String(h).padStart(2, "0")}:00`}</option>)}
                  </select>
                </label>
                <label className="flex flex-col gap-1">
                  <span className={label}>Time zone</span>
                  <select className={`${field} w-full`} value={tz} onChange={(e) => patch({ schedule_tz: e.target.value })}>
                    {zones(tz).map((z) => <option key={z} value={z}>{z.replaceAll("_", " ")}</option>)}
                  </select>
                  <span className={hint}>Pick a quiet hour for your site.</span>
                </label>
              </>
            )}
          </div>
        </section>

        <div className="flex justify-end border-t pt-4"><Button variant="ghost" onClick={remove}>Remove site</Button></div>
      </div>
    </Card>
  );
}

function Code({ text }: { text: string }) {
  return (
    <div className="mt-1 flex items-center gap-2 rounded-md border bg-muted/40 px-2 py-1">
      <code className="flex-1 font-mono text-[12px] break-all">{text}</code>
      <CopyButton text={text} />
    </div>
  );
}

function LatestCrawl({ ws, site, crawl, siteVerified, go }: { ws: string; site: string; crawl: Crawl; siteVerified: boolean; go: (tab: Tab, item?: string) => void }) {
  const [issues, setIssues] = useState<CrawlIssue[] | null>(null);
  const [error, setError] = useState("");
  const done = !running(crawl);
  useEffect(() => {
    if (!done) return;
    api.GET("/v1/workspaces/{ws}/crawls/{crawl}/issues", { params: { path: { ws, crawl: crawl.id } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setIssues(data.data);
    });
  }, [ws, crawl.id, done]);

  return (
    <>
      <Stats cols={5}>
        <Stat label="Health" value={crawl.health_score ?? "–"} sub={done ? "of 100" : "when the crawl ends"} tone={crawl.health_score === null ? undefined : crawl.health_score >= 90 ? "green" : crawl.health_score >= 70 ? "amber" : "red"} />
        <Stat label="Pages read" value={crawl.pages_fetched.toLocaleString()} sub={`of ${crawl.pages_found.toLocaleString()} found`} />
        <Stat label="Errors" value={crawl.issues.errors.toLocaleString()} tone={crawl.issues.errors > 0 ? "red" : undefined} />
        <Stat label="Warnings" value={crawl.issues.warnings.toLocaleString()} tone={crawl.issues.warnings > 0 ? "amber" : undefined} />
        <Stat label="Blocked by robots.txt" value={crawl.pages_blocked.toLocaleString()} />
      </Stats>
      {!done && <p className="text-[13px] text-muted-foreground">{crawlLine(crawl)}. The audit runs when it ends.</p>}
      <p className="text-[12px] text-muted-foreground">
        {done && crawl.reason && <>{reasons[crawl.reason] ?? crawl.reason} </>}
        {done ? "Crawled" : "Crawling"} {settingsLine(crawl)}.
        {crawl.pages_excluded > 0 && <> {plural(crawl.pages_excluded, "link")} left out by your scope settings.</>}
        {crawl.pages_rendered > 0 && <> {plural(crawl.pages_rendered, "page")} read with scripts run, through your Geonode.</>}
      </p>
      {crawl.pages_firewalled > 0 && (
        <div role="status" className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-900 dark:text-amber-200">
          A firewall or bot protection blocked {plural(crawl.pages_firewalled, "page")}
          {crawl.pages_fallback > 0 && <>; {plural(crawl.pages_fallback, "page")} {crawl.pages_fallback === 1 ? "was" : "were"} read through your Geonode connection instead</>}.{" "}
          {crawl.fallback_hint === "verify_site" && "Prove you own the site to read them through your own Geonode connection, or let OutflareBot through the firewall."}
          {crawl.fallback_hint === "connect_geonode" && (
            <>Connect your Geonode account to read them, or let OutflareBot through the firewall. <span className="mt-2 block"><ConnectFor connector="geonode" name="Geonode" back="audits" item={site} why={geonodeWhy.audits} go={go} variant="ghost" /></span></>
          )}
          {!crawl.fallback_hint && siteVerified && crawl.pages_fallback === 0 && "Let OutflareBot through the firewall to read them."}
        </div>
      )}
      {done && (
        <Card title={issues ? `Issues (${issues.length})` : "Issues"} description="Grouped by URL template, so one template fix clears many pages. Each one is also on the fixes list." action={<Button variant="ghost" onClick={() => go("fixes")}>Open fixes</Button>}>
          <ErrorText>{error}</ErrorText>
          {!issues && !error && <Loading />}
          {issues && issues.length === 0 && <p className="text-[13px] text-green-700 dark:text-green-400">No issues. Every page passed every rule.</p>}
          {issues && issues.length > 0 && (
            <ul className="divide-y">
              {issues.map((i) => <IssueRow key={i.id} ws={ws} crawl={crawl.id} issue={i} />)}
            </ul>
          )}
        </Card>
      )}
    </>
  );
}

type IssuePage = { id: string; page_id: string; url: string; template: string; detail: string; http_status: number | null };

function IssueRow({ ws, crawl, issue }: { ws: string; crawl: string; issue: CrawlIssue }) {
  const [open, setOpen] = useState(false);
  const [pages, setPages] = useState<IssuePage[] | null>(null);
  const [template, setTemplate] = useState("");

  useEffect(() => {
    if (!open) return;
    setPages(null);
    api.GET("/v1/workspaces/{ws}/crawls/{crawl}/issues/{rule}", { params: { path: { ws, crawl, rule: issue.id }, query: { ...(template ? { template } : {}), limit: 50 } } }).then(({ data }) => setPages(data?.data ?? []));
  }, [open, ws, crawl, issue.id, template]);

  return (
    <li className="py-3">
      <button className="flex w-full flex-wrap items-center gap-2 text-left" onClick={() => setOpen(!open)} aria-expanded={open}>
        <Badge tone={severityTone[issue.severity]}>{severityLabel[issue.severity]}</Badge>
        <span className="text-[13px] font-medium">{issue.title}</span>
        <span className="text-[13px] text-muted-foreground">{plural(issue.pages, "page")}</span>
        <span className="ml-auto flex flex-wrap gap-1">
          {issue.templates.slice(0, 3).map((t) => <Badge key={t.template}>{t.template} · {t.pages}</Badge>)}
          {issue.templates.length > 3 && <Badge>+{issue.templates.length - 3}</Badge>}
        </span>
      </button>
      {open && (
        <div className="mt-2 space-y-2 pl-1 text-[13px]">
          <p><span className="font-medium">Why it matters:</span> {issue.why}</p>
          <p><span className="font-medium">How to fix it:</span> {issue.fix}</p>
          {issue.templates.length > 1 && (
            <select className={field} aria-label="URL template" value={template} onChange={(e) => setTemplate(e.target.value)}>
              <option value="">Every template</option>
              {issue.templates.map((t) => <option key={t.template} value={t.template}>{t.template} ({t.pages})</option>)}
            </select>
          )}
          {!pages && <Loading />}
          {pages && (
            <ul className="space-y-1">
              {pages.map((p) => (
                <li key={p.id} className="flex flex-wrap items-baseline gap-2">
                  <a className="font-mono text-[12px] break-all underline-offset-2 hover:underline" href={p.url} target="_blank" rel="noreferrer noopener">{p.url}</a>
                  {p.detail && <span className="text-[12px] text-muted-foreground">{p.detail}</span>}
                </li>
              ))}
              {pages.length === 50 && <li className="text-[12px] text-muted-foreground">Showing the first 50. The API and list_crawl_issues page through the rest.</li>}
            </ul>
          )}
        </div>
      )}
    </li>
  );
}
