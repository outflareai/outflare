import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { components } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural } from "../format";
import { SiteKeysCard } from "./SiteKeys";
import type { Tab } from "../App";

// Visitors: the companies and people on the workspace's website, scored for
// buying intent. People are known from the workspace's own sign-in or from
// a click on a tracked link in an email Outflare sent them; their company is
// their first company, else the one with their email's domain. When the
// workspace turns lookups on, unknown visitors' companies come from its own
// data provider, and stay off the CRM until someone adds them.

type Visitor = components["schemas"]["Visitor"];
type Totals = components["schemas"]["VisitorTotals"];
type Settings = components["schemas"]["VisitorSettings"];
type TimelineItem = components["schemas"]["TimelineItem"];

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

const ranges = [7, 30, 90] as const;
const views = [
  { id: "companies", label: "Companies" },
  { id: "people", label: "People" },
] as const;

const field = "h-8 rounded-md border border-input bg-transparent px-2.5 text-[13px]";

const how: Record<string, string> = {
  product: "Signed in",
  email_click: "Email click",
  company_ip: "IP lookup",
  provider: "Data provider",
};

function ScoreBadge({ v }: { v: Visitor }) {
  return (
    <Badge tone={v.hot ? "green" : v.score >= 30 ? "amber" : "grey"} title="Buying intent, 0 to 100: key pages count most, then return visits and reading.">
      {v.hot ? `Hot · ${v.score}` : v.score}
    </Badge>
  );
}

export function Visitors({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const [view, setView] = useState<(typeof views)[number]["id"]>("companies");
  const [days, setDays] = useState<(typeof ranges)[number]>(30);
  const [hotOnly, setHotOnly] = useState(false);
  const [rows, setRows] = useState<Visitor[] | null>(null);
  const [totals, setTotals] = useState<Totals | null>(null);
  const [hasKeys, setHasKeys] = useState<boolean | null>(null);
  const [open, setOpen] = useState<Visitor | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const query = { view, days, ...(hotOnly ? { hot: true } : {}), limit: 200, ...(brandKey ? { brand: brandKey } : {}) };
    const [v, k] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/visitors", { params: { path: { ws }, query } }),
      api.GET("/v1/workspaces/{ws}/site-keys", { params: { path: { ws } } }),
    ]);
    if (v.error || !v.data) return setError(errorMessage(v.error));
    setError("");
    setRows(v.data.data);
    setTotals(v.data.totals);
    setHasKeys((k.data?.data.length ?? 0) > 0);
  }, [ws, view, days, hotOnly, brandKey]);
  useEffect(() => {
    load();
  }, [load]);

  const header = (
    <PageHeader
      title="Visitors"
      description="The companies and people on your website, best first by buying intent. People are known when they sign in to your product or arrive from a tracked link in an email you sent them. Companies can also come from your own data provider's IP lookups, which are often wrong, so they stay off your CRM until you add them. Agents use the visitors tools."
      action={
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex rounded-md border" role="group" aria-label="Date range">
            {ranges.map((r) => (
              <button key={r} type="button" className={`px-2.5 py-1 text-[13px] ${r === days ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setDays(r)}>{r} days</button>
            ))}
          </div>
          {canManage && <Button variant="ghost" onClick={() => setShowSettings((s) => !s)}>{showSettings ? "Hide settings" : "Settings"}</Button>}
        </div>
      }
    />
  );
  if (error && !rows) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!rows || !totals || hasKeys === null) return <div className="space-y-6">{header}<Loading /></div>;

  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      {showSettings && canManage && <SettingsCard ws={ws} onSaved={load} go={go} />}

      {!hasKeys && (
        <>
          <Card title="Add the snippet to your website" description="Visitors are counted from the site snippet. Add a key, paste its script tag into your site's <head>, and visits start showing here. Tracked links in your emails then tell you who is reading.">
            <p className="text-[13px] text-muted-foreground">Nothing is read from anywhere else, and the snippet only records page views.</p>
          </Card>
          <SiteKeysCard ws={ws} canManage={canManage} />
        </>
      )}

      <Stats cols={4}>
        <Stat label={`Visitors, ${days} days`} value={totals.browsers.toLocaleString()}
          sub={totals.no_company > 0 ? `${totals.no_company.toLocaleString()} on home, phone or VPN networks: no company` : "browsers, known or not"} />
        <Stat label="Known people" value={totals.people.toLocaleString()} sub={plural(totals.known, "browser")} />
        <Stat label="Companies" value={totals.companies.toLocaleString()} />
        <Stat label="Hot" value={totals.hot.toLocaleString()} sub={`in ${view}`} tone={totals.hot > 0 ? "green" : undefined} />
      </Stats>

      <DataTable
        data={rows}
        rowId={(r) => r.record_id ?? `domain:${r.domain}`}
        onRowClick={setOpen}
        searchPlaceholder={view === "companies" ? "Search companies" : "Search people"}
        toolbar={
          <div className="flex items-center gap-3">
            <div className="flex rounded-md border" role="group" aria-label="Show">
              {views.map((v) => (
                <button key={v.id} type="button" className={`px-2.5 py-1 text-[13px] ${v.id === view ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setView(v.id)}>{v.label}</button>
              ))}
            </div>
            <label className="flex items-center gap-2 text-[13px] text-muted-foreground">
              <Switch checked={hotOnly} onCheckedChange={setHotOnly} aria-label="Only hot visitors" />
              Only hot
            </label>
          </div>
        }
        empty={
          totals.browsers === 0
            ? "No visits yet. They show here once the snippet is on your site."
            : view === "companies"
              ? "Visitors came, but none is known yet. People become known when they sign in to your product or click a tracked link in your email."
              : "No known people in this range yet."
        }
        mobile={(r) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2"><span className="font-medium">{r.name || r.domain || r.email || "Unnamed"}</span><ScoreBadge v={r} /></div>
            <div className="text-[13px] text-muted-foreground">{plural(r.pageviews, "page view")} · last {ago(r.last_seen_at)}</div>
          </div>
        )}
        columns={[
          {
            id: "name",
            header: view === "companies" ? "Company" : "Person",
            accessorFn: (r) => `${r.name} ${r.domain ?? ""} ${r.email ?? ""} ${r.company?.name ?? ""}`,
            cell: ({ row: { original: r } }) => (
              <div className="min-w-0">
                <div className="flex items-center gap-2 font-medium">
                  {r.name || r.domain || r.email || "Unnamed"}
                  {!r.in_crm && <Badge tone="grey" title="Found by your data provider from visitors' IP addresses. Lookups are often wrong: check it before you add it to your CRM.">Not in CRM</Badge>}
                </div>
                <div className="truncate text-[12px] text-muted-foreground">
                  {r.kind === "company"
                    ? [r.domain, r.people > 0 ? plural(r.people, "person", "people") : ""].filter(Boolean).join(" · ")
                    : [r.job_title, r.company?.name ?? r.email].filter(Boolean).join(" · ")}
                </div>
              </div>
            ),
          },
          { id: "score", header: "Score", accessorFn: (r) => r.score, cell: ({ row: { original: r } }) => <ScoreBadge v={r} /> },
          { id: "key_pageviews", header: "Key pages", accessorFn: (r) => r.key_pageviews },
          { id: "pageviews", header: "Page views", accessorFn: (r) => r.pageviews },
          { id: "sessions", header: "Visits", accessorFn: (r) => r.sessions },
          {
            id: "last_seen",
            header: "Last seen",
            accessorFn: (r) => r.last_seen_at,
            cell: ({ row: { original: r } }) => (
              <div className="min-w-0">
                <div>{ago(r.last_seen_at)}</div>
                <div className="truncate font-mono text-[12px] text-muted-foreground">{r.last_path}</div>
              </div>
            ),
          },
          { id: "how", header: "Known by", accessorFn: (r) => r.identified_by.map((h) => how[h] ?? h).join(", ") },
        ]}
        initialSort={[{ id: "score", desc: true }]}
      />

      {open && <VisitorSheet ws={ws} v={open} canManage={canManage} onClose={() => setOpen(null)} onChanged={() => { setOpen(null); load(); }} go={go} />}
    </div>
  );
}

function VisitorSheet({ ws, v, canManage, onClose, onChanged, go }: {
  ws: string; v: Visitor; canManage: boolean; onClose: () => void; onChanged: () => void; go: (tab: Tab, item?: string) => void;
}) {
  const [pages, setPages] = useState<TimelineItem[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fromLookup = v.kind === "company" && v.identified_by.includes("company_ip") && !!v.domain;

  useEffect(() => {
    if (v.kind !== "person" || !v.record_id) return;
    api.GET("/v1/workspaces/{ws}/records/{record}/timeline", { params: { path: { ws, record: v.record_id }, query: { kinds: "event", limit: 100 } } }).then(({ data }) =>
      setPages((data?.data ?? []).filter((t) => t.title === "page.viewed" && t.source === "browser").slice(0, 25)),
    );
  }, [ws, v]);

  async function addToCRM() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/visitors/companies", { params: { path: { ws } }, body: { domain: v.domain ?? "" } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`${v.name || v.domain} is in your CRM, marked as found by an IP lookup.`);
    onChanged();
  }

  // Marks the domain as not a company, such as an ISP whose customers the
  // provider took for its own: its visitors lose it.
  async function notACompany() {
    setBusy(true);
    const cur = await api.GET("/v1/workspaces/{ws}/visitors/settings", { params: { path: { ws } } });
    if (cur.error || !cur.data) {
      setBusy(false);
      return setError(errorMessage(cur.error));
    }
    const { error } = await api.PUT("/v1/workspaces/{ws}/visitors/settings", {
      params: { path: { ws } },
      body: { not_companies: [...cur.data.not_companies, v.domain ?? ""] },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(`${v.domain} won't be named as a company again.`);
    onChanged();
  }

  const path = (t: TimelineItem) => {
    const ctx = (t.detail as { context?: { page?: { path?: string } } }).context;
    return ctx?.page?.path ?? "";
  };

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{v.name || v.domain || v.email || "Unnamed"}</SheetTitle>
          <SheetDescription>
            {v.kind === "company" ? v.domain : [v.job_title, v.company?.name, v.email].filter(Boolean).join(" · ")}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          <div className="flex items-center gap-2"><ScoreBadge v={v} /><span className="text-muted-foreground">first seen {ago(v.first_seen_at)}, last {ago(v.last_seen_at)}</span></div>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
            <dt className="text-muted-foreground">Page views</dt><dd>{v.pageviews.toLocaleString()}</dd>
            <dt className="text-muted-foreground">Key pages</dt><dd>{v.key_pageviews.toLocaleString()}</dd>
            <dt className="text-muted-foreground">Visits</dt><dd>{v.sessions.toLocaleString()}</dd>
            <dt className="text-muted-foreground">Browsers</dt><dd>{v.browsers.toLocaleString()}</dd>
            <dt className="text-muted-foreground">Known by</dt><dd>{v.identified_by.map((h) => how[h] ?? h).join(", ") || "—"}</dd>
          </dl>
          {v.kind === "company" && v.people_sample.length > 0 && (
            <div className="space-y-1">
              <div className="font-medium">People from {v.name || v.domain}</div>
              <ul className="list-disc pl-5">{v.people_sample.map((p) => <li key={p.id}>{p.name || "Unnamed"}</li>)}</ul>
              {v.people > v.people_sample.length && <div className="text-muted-foreground">and {plural(v.people - v.people_sample.length, "more", "more")}</div>}
            </div>
          )}
          {v.kind === "person" && (
            <div className="space-y-1">
              <div className="font-medium">Recent pages</div>
              {pages === null ? <Loading /> : pages.length === 0 ? <div className="text-muted-foreground">None in their timeline yet.</div> : (
                <ul className="space-y-1">
                  {pages.map((t) => (
                    <li key={t.id} className="flex justify-between gap-3"><span className="truncate font-mono">{path(t)}</span><span className="shrink-0 text-muted-foreground">{ago(t.at)}</span></li>
                  ))}
                </ul>
              )}
            </div>
          )}
          {fromLookup && (
            <div className="space-y-2 rounded-md border p-3">
              <div className="text-muted-foreground">
                {v.in_crm
                  ? "Your data provider matched these visitors to this company from their IP address."
                  : "Found by your data provider from visitors' IP addresses, and not in your CRM. Lookups are often wrong, for example naming an internet provider instead of its customers."}
              </div>
              {canManage && (
                <div className="flex flex-wrap gap-2">
                  {!v.in_crm && <Button onClick={addToCRM} disabled={busy}>Add to CRM</Button>}
                  <Button variant="ghost" onClick={notACompany} disabled={busy}>Not a company (ISP)</Button>
                </div>
              )}
              {error && <ErrorText>{error}</ErrorText>}
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <Button variant="ghost" onClick={() => go("leads")}>Open leads</Button>
            <Button variant="ghost" onClick={() => go("sequences")}>Sequences</Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function SettingsCard({ ws, onSaved, go }: { ws: string; onSaved: () => void; go: (tab: Tab, item?: string) => void }) {
  const [s, setS] = useState<Settings | null>(null);
  const [keyPages, setKeyPages] = useState("");
  const [exclude, setExclude] = useState("");
  const [notCompanies, setNotCompanies] = useState("");
  const [hot, setHot] = useState("60");
  const [limit, setLimit] = useState("500");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/visitors/settings", { params: { path: { ws } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setS(data);
      setKeyPages(data.key_pages.join("\n"));
      setExclude(data.exclude_domains.join("\n"));
      setNotCompanies(data.not_companies.join("\n"));
      setHot(String(data.hot_score));
      setLimit(String(data.reveal_daily_limit));
    });
  }, [ws]);

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!s) return;
    setBusy(true);
    const split = (v: string) => v.split(/[\s,]+/).filter(Boolean);
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/visitors/settings", {
      params: { path: { ws } },
      body: {
        key_pages: split(keyPages), exclude_domains: split(exclude), hot_score: Number(hot), email_clicks: s.email_clicks, exclude_customers: s.exclude_customers,
        reveal_companies: s.reveal_companies, reveal_integration_id: s.reveal_integration_id, reveal_daily_limit: Number(limit), not_companies: split(notCompanies),
      },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setError("");
    setS(data);
    toast("Visitor settings saved.");
    onSaved();
  }

  if (!s) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  return (
    <Card title="Visitor settings" description="What counts as buying intent, when someone is hot, and who is left out.">
      <form onSubmit={save} className="space-y-4">
        <label className="block space-y-1 text-[13px]">
          <span className="font-medium">Key pages</span>
          <textarea className="min-h-24 w-full rounded-md border border-input bg-transparent px-2.5 py-1.5 font-mono text-[13px]" value={keyPages} onChange={(e) => setKeyPages(e.target.value)} />
          <span className="text-muted-foreground">One path per line, such as /pricing. End with * to include everything under it (/pricing*). Views of these count most.</span>
        </label>
        <Input label="Hot from score" type="number" min={1} max={100} value={hot} onChange={(e) => setHot(e.target.value)} hint="1 to 100. A person or company that reaches it gets an alert (webhook visitor.became_hot) and a line in your digest, at most once a week." />
        <label className="block space-y-1 text-[13px]">
          <span className="font-medium">Leave out these domains</span>
          <textarea className="min-h-16 w-full rounded-md border border-input bg-transparent px-2.5 py-1.5 font-mono text-[13px]" value={exclude} onChange={(e) => setExclude(e.target.value)} placeholder="your-company.example" />
          <span className="text-muted-foreground">Your own team and competitors. People with an email there, and companies with that domain, are not listed.</span>
        </label>
        <label className="flex items-start gap-3 text-[13px]">
          <Switch checked={s.email_clicks} onCheckedChange={(v) => setS({ ...s, email_clicks: v })} className="mt-0.5" />
          <span><span className="font-medium">Know people from email clicks.</span> <span className="text-muted-foreground">When someone clicks a tracked link to your site in an email you sent them, their visits join their record. Only links to your brands' domains or your site keys' origins carry it.</span></span>
        </label>
        <label className="flex items-start gap-3 text-[13px]">
          <Switch checked={s.exclude_customers} onCheckedChange={(v) => setS({ ...s, exclude_customers: v })} className="mt-0.5" />
          <span><span className="font-medium">Leave out customers.</span> <span className="text-muted-foreground">People and companies that pay you, or are marked as customers.</span></span>
        </label>
        <div className="space-y-3 border-t pt-4">
          <div className="text-[13px] font-medium">Company lookups</div>
          {s.reveal_connections.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">
              Find the company behind visitors nobody knows yet, with your own People Data Labs or Snitcher account.{" "}
              <button type="button" className="underline" onClick={() => go("connectors")}>Connect one</button> first. Outflare never looks anything up on its own account.
            </p>
          ) : (
            <>
              <label className="flex items-start gap-3 text-[13px]">
                <Switch checked={s.reveal_companies} onCheckedChange={(v) => setS({ ...s, reveal_companies: v })} className="mt-0.5" />
                <span><span className="font-medium">Look up companies from visitors' addresses.</span> <span className="text-muted-foreground">Only visitors who accepted your site's cookies, each address once a week, on your provider's credits. Home, phone, VPN and data-centre connections name nobody. Companies found stay off your CRM until you add them, and are dropped after 90 days without a visit.</span></span>
              </label>
              {s.reveal_connections.length > 1 && (
                <label className="block space-y-1 text-[13px]">
                  <span className="font-medium">Look up with</span>
                  <select className={field + " w-full"} value={s.reveal_integration_id ?? ""} onChange={(e) => setS({ ...s, reveal_integration_id: e.target.value || null })}>
                    <option value="">The first that can</option>
                    {s.reveal_connections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                  </select>
                </label>
              )}
              <Input label="Lookups a day at most" type="number" min={1} max={100000} value={limit} onChange={(e) => setLimit(e.target.value)} hint={`${plural(s.reveals_today, "lookup")} today (UTC).`} />
              <label className="block space-y-1 text-[13px]">
                <span className="font-medium">Not companies</span>
                <textarea className="min-h-16 w-full rounded-md border border-input bg-transparent px-2.5 py-1.5 font-mono text-[13px]" value={notCompanies} onChange={(e) => setNotCompanies(e.target.value)} placeholder="isp.example" />
                <span className="text-muted-foreground">Internet providers and carriers your provider mistook for a company. Their visitors lose them, and they are never named again.</span>
              </label>
            </>
          )}
        </div>
        {error && <ErrorText>{error}</ErrorText>}
        <Button type="submit" disabled={busy}>Save</Button>
      </form>
    </Card>
  );
}
