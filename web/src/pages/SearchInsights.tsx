import { InsightChart } from "./search-insights/InsightChart";
import { GroupEditor, MigrationEditor } from "./search-insights/Editors";
import { TagEditor } from "./search-insights/Primitives";
import { labels, reportFamilies } from "./search-insights/model";
import { InsightTable } from "./search-insights/InsightTable";
import type { Filters, WorkItem } from "./search-insights/model";
import { track } from "../lib/analytics";
import { useCallback, useEffect, useState } from "react";
import type {
  SearchInsightReport,
  SearchInsightRow,
  SearchGroup,
  SearchMigration,
  SEOSite,
  Integration,
} from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Empty, ErrorText, field, Loading, toast } from "../ui";

type Report = SearchInsightReport["report"];
const reports: Record<Report, string> = {
  opportunities: "Opportunities",
  ctr: "CTR benchmarks",
  decay: "Content decay",
  segments: "Countries & devices",
  groups: "Content & topics",
  forecast: "Forecast",
  completeness: "Data completeness",
  changes: "Trend breaks",
  attribution: "GA4 outcomes",
  "ai-referrals": "AI referrals",
  migrations: "Migrations",
  vitals: "Core Web Vitals",
};
const descriptions: Record<Report, string> = {
  opportunities:
    "Prioritise pages and queries by estimated click gains from better snippets and top-three rankings.",
  ctr: "Your observed CTR by ranking position, with the benchmark used to estimate opportunities.",
  decay:
    "Compare complete calendar weeks or months to find gradual declines. Click a row to inspect its history.",
  segments:
    "Find the pages, countries and devices behind search performance. Historical segments build as Search Console syncs.",
  groups:
    "Measure saved URL groups and keyword topics. Groups may overlap, so don't add their totals together.",
  forecast:
    "Project search clicks using weekday seasonality and a bounded trend. Change assumptions to compare scenarios.",
  completeness:
    "Compare property totals with visible page/query rows. Google privacy omissions and collection limits both affect the gap.",
  changes:
    "Sustained traffic changes alongside official Google updates and production deploys. Timing alone does not prove cause.",
  attribution:
    "Measured GA4 landing-page outcomes and estimated query allocation using visible Search Console clicks.",
  "ai-referrals":
    "Measure visits and outcomes from known AI assistant referrers in GA4. Missing referrers cannot be attributed.",
  migrations:
    "Compare settled search traffic around a migration and inspect redirect evidence from stored crawls.",
  vitals:
    "Real-user page experience from Chrome UX Report, including weekly history and regression flags.",
};
const percent = (n: number) =>
  `${(n * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
const number = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: 2 });
export function SearchExplore({
  ws,
  site,
  sites,
  canManage,
  initial,
  onSaveReport,
  onInspect,
  onWorkSaved,
  items = [],
}: {
  ws: string;
  site: SEOSite;
  sites: SEOSite[];
  canManage: boolean;
  initial?: Filters;
  onSaveReport?: (filters: Filters) => void;
  onInspect?: (row: SearchInsightRow, filters: Filters) => void;
  items?: WorkItem[];
  onWorkSaved?: () => void;
}) {
  const [report, setReport] = useState<Report>(
    (initial?.report as Report) || "opportunities",
  );
  const [result, setResult] = useState<SearchInsightReport | null>(null);
  const [error, setError] = useState("");
  const [from, setFrom] = useState(initial?.from ?? "");
  const [to, setTo] = useState(initial?.to ?? "");
  const [country, setCountry] = useState(initial?.country ?? "");
  const [device, setDevice] = useState(initial?.device ?? "");
  const [groups, setGroups] = useState<SearchGroup[]>([]);
  const [group, setGroup] = useState(initial?.group_id ?? "");
  const [migrations, setMigrations] = useState<SearchMigration[]>([]);
  const [migration, setMigration] = useState(initial?.migration_id ?? "");
  const [bucket, setBucket] = useState(initial?.bucket ?? "week");
  const [threshold, setThreshold] = useState((initial?.threshold ?? 0.1) * 100);
  const [weeks, setWeeks] = useState(initial?.weeks ?? 12);
  const [growth, setGrowth] = useState((initial?.growth ?? 0) * 100);
  const [conversionRate, setConversionRate] = useState(
    (initial?.conversion_rate ?? 0) * 100,
  );
  const [orderValue, setOrderValue] = useState(initial?.order_value ?? 0);
  const [currency, setCurrency] = useState(initial?.currency ?? "USD");
  const [version, setVersion] = useState(0);
  const [editGroup, setEditGroup] = useState<SearchGroup>();
  const [editMigration, setEditMigration] = useState<SearchMigration>();
  const [bulkTags, setBulkTags] = useState<string[]>([]);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkError, setBulkError] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [tag, setTag] = useState("");
  const filters: Filters = {
    report,
    from: from || undefined,
    to: to || undefined,
    country: country || undefined,
    device: device || undefined,
    group_id: group || undefined,
    migration_id: migration || undefined,
    integration_id: initial?.integration_id,
    bucket,
    threshold: threshold / 100,
    weeks,
    growth: growth / 100,
    conversion_rate: conversionRate / 100,
    order_value: orderValue,
    currency,
  };
  async function saveSelected() {
    if (!brand || !result) return;
    setBulkBusy(true);
    setBulkError("");
    const failed = new Set<string>();
    const rows = result.data.filter((r) => selected.has(r.key));
    const save = async (row: SearchInsightRow) => {
      try {
        const existing = items.find(
          (x) => x.report === report && x.key === row.key,
        );
        const { error, data } = await api.POST(
          "/v1/brands/{brand}/seo-sites/{site}/insights/items",
          {
            params: { path: { brand, site: site.id } },
            body: {
              report,
              key: row.key,
              filters,
              tags: [
                ...new Set([...(existing?.tags ?? []), ...bulkTags]),
              ].slice(0, 20),
            },
          },
        );
        if (error || !data || !("id" in data)) throw Error(errorMessage(error));
      } catch {
        failed.add(row.key);
      }
    };
    for (let i = 0; i < rows.length; i += 10)
      await Promise.allSettled(rows.slice(i, i + 10).map(save));
    setSelected(failed);
    setBulkBusy(false);
    if (failed.size)
      setBulkError(
        `${failed.size} findings could not be saved. Failed rows remain selected; try saving them again.`,
      );
    else {
      toast("Selected findings saved to Work.");
      setBulkTags([]);
    }
    track("ui.search_work_saved", {
      count: rows.length - failed.size,
      tag_count: bulkTags.length,
    });
    onWorkSaved?.();
  }
  const brand = site.brand_id;
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  useEffect(() => {
    if (!brand) return;
    let current = true;
    api
      .GET("/v1/brands/{brand}/seo-sites/{site}/search-groups", {
        params: { path: { brand, site: site.id } },
      })
      .then(({ data, error }) => {
        if (current) {
          setGroups(data?.data ?? []);
          if (error) setError(errorMessage(error));
        }
      });
    api
      .GET("/v1/brands/{brand}/seo-sites/{site}/search-migrations", {
        params: { path: { brand, site: site.id } },
      })
      .then(({ data, error }) => {
        if (current) {
          setMigrations(data?.data ?? []);
          if (error) setError(errorMessage(error));
        }
      });
    return () => {
      current = false;
    };
  }, [brand, site.id, version]);
  useEffect(() => {
    if (!brand) return;
    let current = true;
    setResult(null);
    setError("");
    const segmented = ["segments", "opportunities", "ctr"].includes(report);
    api
      .GET("/v1/brands/{brand}/seo-sites/{site}/insights", {
        params: {
          path: { brand, site: site.id },
          query: {
            report,
            from: from || undefined,
            to: to || undefined,
            country: segmented && country.length === 3 ? country : undefined,
            device: segmented ? device || undefined : undefined,
            group_id: [
              "opportunities",
              "ctr",
              "decay",
              "segments",
              "groups",
            ].includes(report)
              ? group || undefined
              : undefined,
            bucket,
            threshold: threshold / 100,
            weeks,
            growth: growth / 100,
            conversion_rate: conversionRate / 100,
            order_value: orderValue,
            currency,
            migration_id: migration || undefined,
            integration_id: initial?.integration_id,
          },
        },
      })
      .then(({ data, error }) => {
        if (current) {
          setResult(data ?? null);
          setError(error ? errorMessage(error) : "");
        }
      });
    return () => {
      current = false;
    };
  }, [
    brand,
    site.id,
    report,
    from,
    to,
    country,
    device,
    group,
    bucket,
    threshold,
    weeks,
    growth,
    conversionRate,
    orderValue,
    currency,
    migration,
    version,
  ]);
  if (!brand)
    return (
      <Empty title="Assign this site to a brand">
        Choose a brand for this site under Site audits to use saved analysis and
        reports.
      </Empty>
    );
  const path = { brand, site: site.id };
  async function removeGroup(id: string) {
    if (
      !window.confirm(
        "Delete this group? Reports that use it will need another filter.",
      )
    )
      return;
    const { error } = await api.DELETE(
      "/v1/brands/{brand}/seo-sites/{site}/search-groups/{item}",
      { params: { path: { ...path, item: id } } },
    );
    if (error) return toast(errorMessage(error), "error");
    if (group === id) setGroup("");
    reload();
  }
  async function removeMigration(id: string) {
    if (!window.confirm("Delete this migration definition?")) return;
    const { error } = await api.DELETE(
      "/v1/brands/{brand}/seo-sites/{site}/search-migrations/{item}",
      { params: { path: { ...path, item: id } } },
    );
    if (error) return toast(errorMessage(error), "error");
    if (migration === id) setMigration("");
    reload();
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="text-xs">
          Report
          <select
            className={`${field} mt-1 block w-full`}
            value={report}
            onChange={(e) => {
              const next = e.target.value as Report;
              setReport(next);
              setSelected(new Set());
              setTag("");
              track("ui.search_insight_report_selected", { report: next });
            }}
          >
            {reportFamilies.map((f) => (
              <optgroup key={f.name} label={f.name}>
                {f.reports.map((key) => (
                  <option key={key} value={key}>
                    {reports[key]}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </label>
        <label className="text-xs">
          From
          <input
            type="date"
            className={`${field} mt-1 block w-full`}
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </label>
        <label className="text-xs">
          To
          <input
            type="date"
            className={`${field} mt-1 block w-full`}
            value={to}
            onChange={(e) => setTo(e.target.value)}
          />
        </label>
        <label className="text-xs">
          Period
          <select
            className={`${field} mt-1 block w-full`}
            value={!from && !to ? "auto" : "custom"}
            onChange={(e) => {
              const days = Number(e.target.value);
              if (!days) {
                setFrom("");
                setTo("");
                return;
              }
              const end = new Date();
              end.setUTCDate(end.getUTCDate() - 3);
              const start = new Date(end);
              start.setUTCDate(start.getUTCDate() - days + 1);
              setFrom(start.toISOString().slice(0, 10));
              setTo(end.toISOString().slice(0, 10));
            }}
          >
            <option value="auto">Latest settled period</option>
            <option value="7">Last 7 settled days</option>
            <option value="28">Last 28 settled days</option>
            <option value="90">Last 90 settled days</option>
            <option value="custom" disabled>
              Custom dates
            </option>
          </select>
        </label>
        {(from || to || country || device || group) && (
          <Button
            variant="ghost"
            onClick={() => {
              setFrom("");
              setTo("");
              setCountry("");
              setDevice("");
              setGroup("");
              setTag("");
              setSelected(new Set());
            }}
          >
            Clear filters
          </Button>
        )}
        {onSaveReport && canManage && (
          <Button onClick={() => onSaveReport(filters)}>Save report</Button>
        )}
        <Button variant="ghost" onClick={reload}>
          Refresh report
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">{descriptions[report]}</p>
      {["opportunities", "ctr", "decay", "segments", "groups"].includes(
        report,
      ) && (
        <label className="block max-w-sm text-xs">
          Saved group
          <select
            className={`${field} mt-1 block w-full`}
            value={group}
            onChange={(e) => setGroup(e.target.value)}
          >
            <option value="">All content</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name} ({g.kind})
              </option>
            ))}
          </select>
        </label>
      )}
      {["segments", "opportunities", "ctr"].includes(report) && (
        <div className="flex flex-wrap gap-3">
          <label className="text-xs">
            Country code
            <input
              className={`${field} mt-1 block w-full`}
              placeholder="e.g. usa"
              maxLength={3}
              value={country}
              onChange={(e) => setCountry(e.target.value.toLowerCase())}
            />
          </label>
          <label className="text-xs">
            Device
            <select
              className={`${field} mt-1 block w-full`}
              value={device}
              onChange={(e) => setDevice(e.target.value)}
            >
              <option value="">All devices</option>
              {["MOBILE", "DESKTOP", "TABLET"].map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
        </div>
      )}
      {report === "decay" && (
        <div className="flex gap-3">
          <label className="text-xs">
            Bucket
            <select
              className={`${field} mt-1 block w-full`}
              value={bucket}
              onChange={(e) => setBucket(e.target.value)}
            >
              <option value="week">Complete weeks</option>
              <option value="month">Complete months</option>
            </select>
          </label>
          <NumberField
            label="Minimum decline (%)"
            value={threshold}
            set={setThreshold}
            min={0}
            max={100}
          />
        </div>
      )}
      {report === "forecast" && (
        <div className="flex flex-wrap gap-3">
          <NumberField
            label="Weeks ahead"
            value={weeks}
            set={setWeeks}
            min={1}
            max={52}
          />
          <NumberField
            label="Annual growth (%)"
            value={growth}
            set={setGrowth}
            min={-90}
            max={500}
          />
          <NumberField
            label="Conversion rate (%)"
            value={conversionRate}
            set={setConversionRate}
            min={0}
            max={100}
          />
          <label className="text-xs">
            Currency
            <input
              className={`${field} mt-1 block w-full`}
              value={currency}
              maxLength={3}
              onChange={(e) => setCurrency(e.target.value.toUpperCase())}
            />
          </label>
          <NumberField
            label={`Order value (${currency})`}
            value={orderValue}
            set={setOrderValue}
            min={0}
            max={1000000}
          />
        </div>
      )}
      {report === "groups" && (
        <div className="space-y-3">
          {canManage && (
            <details className="rounded border p-4" open={!!editGroup}>
              <summary className="cursor-pointer text-sm font-medium">
                {editGroup ? "Edit group" : "Create a content or topic group"}
              </summary>
              <div className="mt-4">
                <GroupEditor
                  key={editGroup?.id ?? "new"}
                  path={path}
                  current={editGroup}
                  onCancel={() => setEditGroup(undefined)}
                  onSaved={() => {
                    setEditGroup(undefined);
                    reload();
                  }}
                />
              </div>
            </details>
          )}
          {groups.map((g) => (
            <div
              key={g.id}
              className="flex items-center justify-between rounded border p-3 text-sm"
            >
              <span>
                {g.name} · {g.kind} ·{" "}
                {g.rules.map((r) => `${r.operator}: ${r.value}`).join("; ")}
              </span>
              {canManage && g.id && (
                <div className="flex gap-2">
                  <Button variant="ghost" onClick={() => setEditGroup(g)}>
                    Edit
                  </Button>
                  <Button variant="ghost" onClick={() => removeGroup(g.id!)}>
                    Delete
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      {report === "migrations" && (
        <div className="space-y-3">
          <label className="block max-w-md text-xs">
            Migration
            <select
              className={`${field} mt-1 block w-full`}
              value={migration}
              onChange={(e) => setMigration(e.target.value)}
            >
              <option value="">Latest migration</option>
              {migrations.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name} · {m.go_live}
                </option>
              ))}
            </select>
          </label>
          {canManage && (
            <details className="rounded border p-4" open={!!editMigration}>
              <summary className="cursor-pointer text-sm font-medium">
                {editMigration ? "Edit migration" : "Set up a migration"}
              </summary>
              <div className="mt-4">
                <MigrationEditor
                  key={editMigration?.id ?? "new"}
                  path={path}
                  current={editMigration}
                  sites={sites.filter((s) => s.brand_id === brand)}
                  onCancel={() => setEditMigration(undefined)}
                  onSaved={() => {
                    setEditMigration(undefined);
                    reload();
                  }}
                />
              </div>
            </details>
          )}
          {canManage &&
            migrations.map((m) => (
              <div
                key={m.id}
                className="flex items-center justify-between text-sm"
              >
                <span>{m.name}</span>
                <Button variant="ghost" onClick={() => setEditMigration(m)}>
                  Edit
                </Button>
                <Button variant="ghost" onClick={() => removeMigration(m.id!)}>
                  Delete
                </Button>
              </div>
            ))}
        </div>
      )}
      {report === "changes" && canManage && (
        <Button
          onClick={async () => {
            const { error } = await api.POST(
              "/v1/brands/{brand}/seo-sites/{site}/insights/refresh",
              { params: { path }, body: {} },
            );
            if (error) return toast(errorMessage(error), "error");
            toast("Google update collection queued.");
          }}
        >
          Collect Google updates
        </Button>
      )}
      {report === "vitals" && canManage && (
        <VitalsForm ws={ws} path={path} host={site.host} onQueued={reload} />
      )}
      {bulkError && (
        <p
          role="alert"
          className="rounded border border-destructive/30 p-3 text-sm text-destructive"
        >
          {bulkError}
        </p>
      )}
      <ErrorText>{error}</ErrorText>
      {!result && !error && <Loading />}
      {result && (
        <>
          <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
            <span>{result.status.replaceAll("_", " ")}</span>
            {result.from && (
              <span>
                {result.from} – {result.to}
              </span>
            )}
          </div>
          {Object.keys(result.summary).length > 0 && (
            <dl className="flex flex-wrap gap-5 rounded border p-3">
              {Object.entries(result.summary).map(([k, v]) => (
                <div key={k}>
                  <dt className="text-xs text-muted-foreground">
                    {labels[k] ?? k.replaceAll("_", " ")}
                  </dt>
                  <dd className="font-mono text-sm">
                    {["ai_share", "recovery", "url_success"].includes(k)
                      ? percent(v)
                      : number(v)}
                  </dd>
                </div>
              ))}
            </dl>
          )}
          {result.notes.map((n) => (
            <p key={n} className="text-xs text-muted-foreground">
              {n}
            </p>
          ))}
          {result.data.length === 0 ? (
            <Empty title="No results for this report">
              {result.status === "not_connected"
                ? "Connect the requested tool and allow its first sync to finish."
                : "Try a longer date range or adjust the filters. Some reports need several weeks of settled history."}
            </Empty>
          ) : (
            <>
              {["forecast", "vitals", "decay", "changes"].includes(report) && (
                <InsightChart report={report} rows={result.data} />
              )}
              <div className="flex flex-wrap items-center gap-2">
                <label className="text-xs">
                  Work tag
                  <select
                    className={`${field} mt-1 block w-full`}
                    value={tag}
                    onChange={(e) => setTag(e.target.value)}
                  >
                    <option value="">All tags</option>
                    {[...new Set(items.flatMap((x) => x.tags))].map((t) => (
                      <option key={t}>{t}</option>
                    ))}
                  </select>
                </label>
                {selected.size > 0 && (
                  <>
                    <span className="text-sm">{selected.size} selected</span>
                    <Button
                      variant="ghost"
                      onClick={() => setSelected(new Set())}
                    >
                      Clear selection
                    </Button>
                    {canManage && (
                      <Button
                        disabled={bulkBusy}
                        onClick={() => void saveSelected()}
                      >
                        {bulkBusy ? "Saving…" : "Save selected to work"}
                      </Button>
                    )}
                  </>
                )}
              </div>
              {selected.size > 0 && canManage && (
                <TagEditor
                  tags={bulkTags}
                  onChange={setBulkTags}
                  label="Tags to add to selected findings"
                />
              )}
              <InsightTable
                report={report}
                rows={result.data.filter(
                  (r) =>
                    !tag ||
                    items.some(
                      (x) =>
                        x.report === report &&
                        x.key === r.key &&
                        x.tags.includes(tag),
                    ),
                )}
                selected={selected}
                setSelected={setSelected}
                onOpen={(row) => onInspect?.(row, filters)}
                tagsFor={(key) =>
                  items.find((x) => x.report === report && x.key === key)
                    ?.tags ?? []
                }
              />
            </>
          )}
        </>
      )}
    </div>
  );
}
function NumberField({
  label,
  value,
  set,
  min,
  max,
}: {
  label: string;
  value: number;
  set: (v: number) => void;
  min: number;
  max: number;
}) {
  return (
    <label className="text-xs">
      {label}
      <input
        type="number"
        className={`${field} mt-1 block w-full`}
        min={min}
        max={max}
        step="any"
        value={value}
        onChange={(e) => set(Number(e.target.value))}
      />
    </label>
  );
}
function VitalsForm({
  ws,
  path,
  host,
  onQueued,
}: {
  ws: string;
  path: { brand: string; site: string };
  host: string;
  onQueued: () => void;
}) {
  const [connections, setConnections] = useState<Integration[]>([]);
  const [connection, setConnection] = useState("");
  const [page, setPage] = useState(`https://${host}`);
  const [device, setDevice] = useState<"PHONE" | "DESKTOP" | "TABLET">("PHONE");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let current = true;
    api
      .GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } })
      .then(({ data, error }) => {
        if (!current) return;
        if (error) return toast(errorMessage(error), "error");
        const cs = (data?.data ?? []).filter(
          (c) => c.connector === "google_crux" && c.status === "connected",
        );
        setConnections(cs);
        setConnection(cs[0]?.id ?? "");
      });
    return () => {
      current = false;
    };
  }, [ws]);
  async function queue(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const { error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/insights/refresh",
        {
          params: { path },
          body: { integration_id: connection, page, device },
        },
      );
      if (error) return toast(errorMessage(error), "error");
      toast("Collection queued. Refresh the report after the job completes.");
      onQueued();
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={queue} className="space-y-3 rounded border p-4">
      <p className="text-sm">
        Connect your own Chrome UX Report API key under Connectors, then collect
        field history. This is a one-time collection. Use Automations to set a
        recurring schedule.
      </p>
      {connections.length > 0 && (
        <>
          <div className="flex flex-wrap gap-3">
            <label className="text-xs">
              Connection
              <select
                className={`${field} mt-1 block w-full`}
                value={connection}
                onChange={(e) => setConnection(e.target.value)}
              >
                {connections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-xs">
              Page or origin
              <input
                type="url"
                required
                className={`${field} mt-1 block w-full`}
                value={page}
                onChange={(e) => setPage(e.target.value)}
              />
            </label>
            <label className="text-xs">
              Device
              <select
                className={`${field} mt-1 block w-full`}
                value={device}
                onChange={(e) => setDevice(e.target.value as typeof device)}
              >
                {["PHONE", "DESKTOP", "TABLET"].map((v) => (
                  <option key={v}>{v}</option>
                ))}
              </select>
            </label>
          </div>
          <Button disabled={busy} type="submit">
            {busy ? "Queueing…" : "Collect history"}
          </Button>
        </>
      )}
    </form>
  );
}
