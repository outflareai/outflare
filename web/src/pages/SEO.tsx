import { useCallback, useEffect, useState } from "react";
import type { SEOHistoryRow, SEOLatest, SEOMetrics, SEOSource } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import type { Tab } from "../App";
import { ago } from "../format";
import { Icon } from "../icons";

// SEO numbers from DataForSEO and Ahrefs: a strip per website from the
// weekly snapshots, the keywords behind it, and each connection's domains
// and budget. Snapshots are paid, so the page only ever reads stored ones.

const whole = (v?: number | null) => (v === undefined || v === null ? "–" : Math.round(v).toLocaleString());

export function money(unit: string, v: number) {
  return unit === "USD" ? `$${v.toFixed(2)}` : `${Math.round(v).toLocaleString()} API units`;
}

function day(d: string) {
  return new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
}

const figures: { key: keyof SEOMetrics; label: string; format?: (v: number) => string }[] = [
  { key: "domain_rating", label: "Domain rating", format: (v) => v.toFixed(0) },
  { key: "referring_domains", label: "Referring domains" },
  { key: "backlinks", label: "Backlinks" },
  { key: "organic_keywords", label: "Ranking keywords" },
  { key: "top3", label: "In the top 3" },
  { key: "organic_traffic", label: "Est. visits / mo" },
  { key: "traffic_value", label: "Traffic value / mo", format: (v) => `$${Math.round(v).toLocaleString()}` },
];

// One line per SEO tool for a website, from its latest snapshot.
export function SEOStrip({ ws, host, seo }: { ws: string; host: string; seo: SEOLatest[] }) {
  const [open, setOpen] = useState(false);
  if (seo.length === 0) return null;
  return (
    <div className="border-t">
      {seo.map((s) => {
        const shown = figures.filter((f) => s[f.key] !== undefined && s[f.key] !== null);
        return (
          <div key={s.integration_id} className="flex flex-wrap items-start gap-x-6 gap-y-2 px-4 py-3">
            <div className="w-40 shrink-0">
              <div className="text-[13px] font-medium">{s.name}</div>
              <div className="text-[12px] text-muted-foreground">SEO · snapshot {day(s.day)}</div>
            </div>
            <dl className="grid flex-1 grid-cols-2 gap-x-6 gap-y-1 sm:grid-cols-4 lg:grid-cols-7">
              {shown.map((f) => (
                <div key={f.key}>
                  <dt className="text-[11px] text-muted-foreground">{f.label}</dt>
                  <dd className="font-mono text-[13px] tabular-nums">{f.format ? f.format(s[f.key] as number) : whole(s[f.key] as number)}</dd>
                </div>
              ))}
            </dl>
          </div>
        );
      })}
      <div className="px-4 pb-3">
        <Button variant="ghost" onClick={() => setOpen(!open)} aria-expanded={open}>{open ? "Hide keywords" : "Keywords this site ranks for"}</Button>
      </div>
      {open && <Keywords ws={ws} host={host} />}
    </div>
  );
}

function Keywords({ ws, host }: { ws: string; host: string }) {
  const [rows, setRows] = useState<SEOHistoryRow[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo/history", { params: { path: { ws }, query: { report: "keywords", host, limit: 100 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
    });
  }, [ws, host]);
  if (error) return <div className="px-4 pb-4"><ErrorText>{error}</ErrorText></div>;
  if (!rows) return <div className="px-4 pb-4"><Loading /></div>;
  if (rows.length === 0) return <p className="px-4 pb-4 text-[13px] text-muted-foreground">No keywords stored yet; they come with the next snapshot.</p>;
  return (
    <div className="overflow-x-auto border-t">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-b text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
            <th className="px-4 py-2 font-medium">Keyword</th>
            <th className="px-4 py-2 text-right font-medium">Position</th>
            <th className="px-4 py-2 text-right font-medium">Change</th>
            <th className="px-4 py-2 text-right font-medium">Searches / mo</th>
            <th className="px-4 py-2 text-right font-medium">Visits / mo (est.)</th>
            <th className="px-4 py-2 font-medium">Page</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const now = r.position, before = r.previous?.position;
            const change = now !== undefined && before !== undefined ? before - now : null;
            return (
              <tr key={r.integration_id + r.key} className="border-b last:border-0">
                <td className="max-w-72 truncate px-4 py-2 font-medium" title={r.key}>{r.key}</td>
                <td className="px-4 py-2 text-right font-mono tabular-nums">{whole(now)}</td>
                <td className="px-4 py-2 text-right font-mono text-[12px] tabular-nums">
                  {change === null ? <span className="text-muted-foreground">{r.previous ? "–" : "new"}</span> : change === 0 ? <span className="text-muted-foreground">0</span> : (
                    <span className={change > 0 ? "text-emerald-700 dark:text-emerald-400" : "text-red-700 dark:text-red-400"}>
                      {change > 0 ? "▲" : "▼"} {Math.abs(change)}
                    </span>
                  )}
                </td>
                <td className="px-4 py-2 text-right font-mono tabular-nums">{whole(r.volume)}</td>
                <td className="px-4 py-2 text-right font-mono tabular-nums">{whole(r.traffic)}</td>
                <td className="max-w-64 truncate px-4 py-2 text-[12px] text-muted-foreground" title={r.url}>{r.url ? r.url.replace(/^https?:\/\/(www\.)?/, "") : ""}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// Each SEO connection: what it follows, its budget, and a snapshot button.
export function SEOTools({ ws, sources, canManage, onChanged }: { ws: string; sources: SEOSource[]; canManage: boolean; onChanged: () => void }) {
  return (
    <section aria-label="SEO tools" className="space-y-3">
      <h2 className="text-[13px] font-medium">SEO tools <span className="font-normal text-muted-foreground">· a snapshot of each domain every week, paid per call within a monthly budget</span></h2>
      {sources.map((s) => <SEOTool key={s.id} ws={ws} source={s} canManage={canManage} onChanged={onChanged} />)}
    </section>
  );
}

function SEOTool({ ws, source, canManage, onChanged }: { ws: string; source: SEOSource; canManage: boolean; onChanged: () => void }) {
  const [editing, setEditing] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const b = source.budget;
  const used = b.limit > 0 ? Math.min(1, b.spent / b.limit) : 1;
  const failed = source.domains.filter((d) => d.last_error);
  const last = source.domains.map((d) => d.last_ok_at).filter(Boolean).sort().pop();

  async function sync() {
    setSyncing(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/seo/{id}/sync", { params: { path: { ws, id: source.id } } });
    setSyncing(false);
    if (error || !data) {
      toast(errorMessage(error), "error");
    } else {
      toast(`Snapshot stored for ${data.domains.join(", ")}. It cost ${money(data.unit, data.cost)}.`);
    }
    onChanged();
  }

  return (
    <div className="rounded-lg border bg-card shadow-xs">
      <div className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
        <div className="min-w-0 space-y-1">
          <div className="font-medium">{source.name}</div>
          <div className="text-[13px] text-muted-foreground">
            {source.domains.length === 0
              ? "No domains yet: add some, or connect an analytics tool and its domains are followed."
              : <>Follows {source.domains.map((d) => d.host).join(", ")}{source.domains_set ? "" : " (from your analytics tools)"} · {source.country} · {source.sync === "off" ? "weekly snapshots off" : last ? `last snapshot ${ago(last)}` : "first snapshot within the hour"}</>}
          </div>
        </div>
        {canManage && (
          <div className="flex gap-1.5">
            <Button variant="ghost" onClick={() => setEditing(!editing)} aria-expanded={editing}>Settings</Button>
            <Button variant="ghost" onClick={sync} disabled={syncing || source.domains.length === 0} title="Store each domain's overview and top keywords now; this costs money">
              <Icon name="refresh" className="size-3.5" /> {syncing ? "Taking snapshot…" : "Snapshot now"}
            </Button>
          </div>
        )}
      </div>
      <div className="px-4 pb-3">
        {b.unlimited ? (
          <div className="text-[12px]">{money(b.unit, b.spent)} spent this month · no monthly limit (change it on the connection's page under Connectors)</div>
        ) : (
          <>
            <div className="flex items-center justify-between text-[12px]">
              <span>{money(b.unit, b.spent)} of {money(b.unit, b.limit)} spent this month{b.default ? " (default budget)" : ""}</span>
              <span className="text-muted-foreground">{money(b.unit, Math.max(0, b.limit - b.spent))} left</span>
            </div>
            <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted" role="meter" aria-valuemin={0} aria-valuemax={b.limit} aria-valuenow={b.spent} aria-label="Budget used">
              <div className={`h-full rounded-full ${used >= 0.9 ? "bg-amber-500" : "bg-brand"}`} style={{ width: `${used * 100}%` }} />
            </div>
          </>
        )}
      </div>
      {failed.length > 0 && (
        <ul className="mx-4 mb-3 space-y-1 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
          {failed.map((d) => <li key={d.host}>{d.host}: {d.last_error}</li>)}
        </ul>
      )}
      {editing && <Settings ws={ws} source={source} onSaved={() => { setEditing(false); onChanged(); }} />}
    </div>
  );
}

function Settings({ ws, source, onSaved }: { ws: string; source: SEOSource; onSaved: () => void }) {
  const [domains, setDomains] = useState(source.domains_set ? source.domains.map((d) => d.host).join(", ") : "");
  const [country, setCountry] = useState(source.country);
  const [budget, setBudget] = useState(source.budget.default || source.budget.unlimited ? "" : String(source.budget.limit));
  const [sync, setSync] = useState(source.sync);
  const [linkRows, setLinkRows] = useState(String(source.link_rows));
  const [linkTraffic, setLinkTraffic] = useState(source.link_traffic);
  const [saving, setSaving] = useState(false);
  const readsLinks = source.link_rows > 0;
  const ahrefs = source.connector === "ahrefs";
  const unit = source.budget.unit;

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    const list = domains.split(/[\s,]+/).map((d) => d.trim()).filter(Boolean);
    const { error } = await api.PUT("/v1/workspaces/{ws}/seo/{id}/settings", {
      params: { path: { ws, id: source.id } },
      body: {
        domains: list.length ? list : null,
        country,
        monthly_budget: budget.trim() === "" ? null : Number(budget),
        sync: sync as "weekly" | "off",
        ...(readsLinks ? { link_rows: Number(linkRows) || null } : {}),
        ...(readsLinks && ahrefs ? { link_traffic: linkTraffic } : {}),
      },
    });
    setSaving(false);
    if (error) return toast(errorMessage(error), "error");
    toast("SEO settings saved");
    onSaved();
  }

  return (
    <form onSubmit={save} className="grid gap-3 border-t px-4 py-3 text-[13px] sm:grid-cols-2">
      <label className="space-y-1 sm:col-span-2">
        <span className="font-medium">Domains</span>
        <input className={`${field} w-full`} placeholder="Leave empty to follow your analytics tools' domains" value={domains} onChange={(e) => setDomains(e.target.value)} />
        <span className="block text-[12px] text-muted-foreground">Separate with commas. Competitors' domains work too; each one costs a snapshot a week.</span>
      </label>
      <label className="space-y-1">
        <span className="font-medium">Country</span>
        <input className={`${field} w-full uppercase`} maxLength={2} value={country} onChange={(e) => setCountry(e.target.value.toUpperCase())} />
      </label>
      <label className="space-y-1">
        <span className="font-medium">Monthly budget ({unit === "USD" ? "US dollars" : "API units"})</span>
        <input className={`${field} w-full`} type="number" min={0} step={unit === "USD" ? 1 : 1000} placeholder={unit === "USD" ? "10 (default)" : "10000 (default)"} value={budget} onChange={(e) => setBudget(e.target.value)} />
      </label>
      <label className="space-y-1">
        <span className="font-medium">Weekly snapshots</span>
        <select className={`${field} w-full`} value={sync} onChange={(e) => setSync(e.target.value as "weekly" | "off")}>
          <option value="weekly">On</option>
          <option value="off">Off</option>
        </select>
      </label>
      {readsLinks && (
        <label className="space-y-1">
          <span className="font-medium">Backlinks read a week, per site</span>
          <input className={`${field} w-full`} type="number" min={1} max={1000} step={50} value={linkRows} onChange={(e) => setLinkRows(e.target.value)} />
          <span className="block text-[12px] text-muted-foreground">Strongest linking domains first. {ahrefs ? "About 1 Ahrefs unit a link." : "About $0.00003 a link."}</span>
        </label>
      )}
      {readsLinks && ahrefs && (
        <label className="flex items-start gap-2 sm:col-span-2">
          <input type="checkbox" className="mt-0.5" checked={linkTraffic} onChange={(e) => setLinkTraffic(e.target.checked)} />
          <span>
            <span className="font-medium">Read traffic for each backlink</span>
            <span className="block text-[12px] text-muted-foreground">
              Fills domain traffic, page traffic and referring domains on Backlinks. Ahrefs charges about 25 more units a link: {(25 * (Number(linkRows) || 200)).toLocaleString()} units a site each week.
            </span>
          </span>
        </label>
      )}
      <div className="flex items-end justify-end sm:col-span-1">
        <Button type="submit" disabled={saving}>{saving ? "Saving…" : "Save"}</Button>
      </div>
    </form>
  );
}

// The SEO page: each SEO tool with its budget, then the keywords each
// followed domain ranks for.
export function SEO({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab, item?: string) => void }) {
  const [sources, setSources] = useState<SEOSource[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/seo", { params: { path: { ws } } });
    if (error || !data) return setError(errorMessage(error));
    setSources(data.data);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  const header = (
    <PageHeader
      title="Rankings & backlinks"
      description="Domain rating, backlinks and the keywords your sites rank for, from a weekly snapshot by DataForSEO or Ahrefs, paid per call within a monthly budget you set. Visitors and search queries are on Website analytics."
      action={sources && sources.length > 0 && canManage ? <Button variant="ghost" onClick={() => go("connectors", "seo")}><Icon name="plus" className="size-3.5" /> Connect another</Button> : undefined}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sources) return <div className="space-y-6">{header}<Loading /></div>;
  if (sources.length === 0) {
    return (
      <div className="space-y-6">
        {header}
        <Empty
          title="Connect an SEO tool"
          action={canManage ? <Button onClick={() => go("connectors", "seo")}>Choose a tool</Button> : <p className="text-[13px] text-muted-foreground">Ask a workspace admin to connect one.</p>}
        >
          DataForSEO or Ahrefs, with an API key. Outflare follows your website's domains (and any competitors you add) and stores a snapshot each week, so looking back costs nothing.
        </Empty>
      </div>
    );
  }
  const hosts = [...new Set(sources.flatMap((s) => s.domains.map((d) => d.host)))];
  return (
    <div className="space-y-6">
      {header}
      <SEOTools ws={ws} sources={sources} canManage={canManage} onChanged={load} />
      {hosts.map((host) => (
        <section key={host} aria-label={`Keywords for ${host}`} className="overflow-hidden rounded-lg border bg-card shadow-xs">
          <h2 className="px-4 py-3 text-[13px] font-medium">Keywords {host} ranks for</h2>
          <Keywords ws={ws} host={host} />
        </section>
      ))}
    </div>
  );
}
