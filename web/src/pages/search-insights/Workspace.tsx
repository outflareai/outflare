import { Recommendations } from "./Recommendations";
import { useCallback, useEffect, useState } from "react";
import type { SEOSite, Integration, SearchInsightRow } from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Button, Empty, Loading, field, toast } from "../../ui";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { SearchExplore } from "../SearchInsights";
import type { Tab } from "../../lib/paths";
import { track } from "../../lib/analytics";
import {
  reports,
  findingAdvice,
  type Filters,
  type WorkItem,
  type Workspace,
  type SavedReport,
  type ReportRun,
  type Schedule,
  type Area,
  areas,
  type Report,
} from "./model";
import { Tags, StateBadge, ErrorRecovery } from "./Primitives";
import { ReportEditor, ScheduleEditor } from "./Editors";
import { InsightTable } from "./InsightTable";
import { Finding } from "./Finding";
const when = (s?: string | null) =>
  s ? new Date(s).toLocaleString() : "Never";
const panel = "rounded-xl border bg-card p-5 space-y-3";
export function SearchInsights({
  ws,
  site,
  sites,
  canManage,
  go,
}: {
  ws: string;
  site: SEOSite;
  sites: SEOSite[];
  canManage: boolean;
  go: (tab: Tab, item?: string) => void;
}) {
  const [area, setArea] = useState<Area>(() => {
    const view = new URLSearchParams(location.search).get("search_view");
    return view && view in areas ? (view as Area) : "overview";
  });
  const [data, setData] = useState<Workspace>();
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  const [busy, setBusy] = useState("");
  const [connections, setConnections] = useState<Integration[]>([]);
  const [initial, setInitial] = useState<Filters>();
  const [editingReport, setEditingReport] = useState<SavedReport>();
  const [exploreKey, setExploreKey] = useState(0);
  const [reportEditor, setReportEditor] = useState<{
    filters: Filters;
    current?: SavedReport;
  }>();
  const [scheduleEditor, setScheduleEditor] = useState<{
    current?: Schedule;
  }>();
  const [finding, setFinding] = useState<{
    row: SearchInsightRow;
    filters: Filters;
    item?: WorkItem;
  }>();
  const [snapshot, setSnapshot] = useState<ReportRun>();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [tag, setTag] = useState("");
  const path = { brand: site.brand_id ?? "", site: site.id };
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  useEffect(() => {
    if (!path.brand) return;
    let live = true;
    api
      .GET("/v1/brands/{brand}/seo-sites/{site}/insights/workspace", {
        params: { path },
      })
      .then(({ data, error }) => {
        if (live) {
          if (error) setError(errorMessage(error));
          else {
            setData(data);
            setError("");
          }
        }
      })
      .catch(() => {
        if (live) setError("Could not load the search workspace.");
      });
    return () => {
      live = false;
    };
  }, [path.brand, path.site, version]);
  useEffect(() => {
    if (!canManage) return;
    let live = true;
    api
      .GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws } } })
      .then(({ data }) => {
        if (live)
          setConnections(
            (data?.data ?? []).filter(
              (c) => c.connector === "google_crux" && c.status === "connected",
            ),
          );
      });
    return () => {
      live = false;
    };
  }, [ws, canManage]);
  useEffect(() => {
    if (!data?.activity.some((x) => ["queued", "running"].includes(x.status)))
      return;
    const timer = setTimeout(reload, 10000);
    return () => clearTimeout(timer);
  }, [data, reload]);
  async function openSnapshot(run: ReportRun) {
    setSnapshot(run);
    if (run.result || run.status === "failed") return;
    await act("snapshot", async () => {
      const { data, error } = await api.GET(
        "/v1/brands/{brand}/seo-sites/{site}/insights/report-runs/{item}",
        { params: { path: { ...path, item: run.id } } },
      );
      if (error || !data) throw Error(errorMessage(error));
      setSnapshot(data);
    });
  }
  function navigate(a: Area) {
    setArea(a);
    const url = new URL(location.href);
    url.searchParams.set("search_view", a);
    history.replaceState(history.state, "", url);
    setSearch("");
    setStatus("");
    setTag("");
    track("ui.search_workspace_opened", { area: a });
  }
  function explore(filters: Filters, saved?: SavedReport) {
    setEditingReport(saved);
    setInitial(filters);
    setExploreKey((v) => v + 1);
    navigate("explore");
  }
  function inspect(row: SearchInsightRow, filters: Filters, item?: WorkItem) {
    setFinding({
      row,
      filters,
      item:
        item ??
        data?.items.find(
          (x) => x.report === filters.report && x.key === row.key,
        ),
    });
    track("ui.search_finding_opened", {
      report: filters.report ?? "opportunities",
    });
  }
  async function act(id: string, fn: () => Promise<void>) {
    setBusy(id);
    setError("");
    try {
      await fn();
      reload();
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "The action failed. Try again.",
      );
    } finally {
      setBusy("");
    }
  }
  async function run(p: SavedReport) {
    await act(p.id, async () => {
      const { data, error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/insights/reports/{item}/run",
        { params: { path: { ...path, item: p.id } } },
      );
      if (error || !data) throw Error(errorMessage(error));
      if ("id" in data) {
        setSnapshot(data);
        track("ui.search_report_run", { status: data.status });
      }
    });
  }
  async function collect(s?: Schedule) {
    await act(s?.id ?? "collect", async () => {
      const { error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/insights/refresh",
        {
          params: { path },
          body: {
            integration_id: s?.integration_id || undefined,
            page: s?.page || undefined,
            device: (s?.device || undefined) as
              | "PHONE"
              | "DESKTOP"
              | "TABLET"
              | undefined,
          },
        },
      );
      if (error) throw Error(errorMessage(error));
      toast("Collection queued. Follow its progress in Activity.");
      track("ui.search_collection_queued", {
        kind: s?.integration_id ? "vitals" : "updates",
      });
      navigate("activity");
    });
  }
  async function pause(s: Schedule) {
    await act(s.id, async () => {
      const { error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/insights/schedules",
        {
          params: { path },
          body: { ...s, frequency: s.frequency === "off" ? "weekly" : "off" },
        },
      );
      if (error) throw Error(errorMessage(error));
      track("ui.search_schedule_changed", {
        frequency: s.frequency === "off" ? "weekly" : "off",
      });
    });
  }
  async function remove(kind: "report" | "schedule", id: string) {
    if (
      !window.confirm(
        kind === "report"
          ? "Delete this report and its saved snapshots? This cannot be undone."
          : "Delete this collection schedule? Its run history will remain.",
      )
    )
      return;
    await act(id, async () => {
      const { error } =
        kind === "report"
          ? await api.DELETE(
              "/v1/brands/{brand}/seo-sites/{site}/insights/reports/{item}",
              { params: { path: { ...path, item: id } } },
            )
          : await api.DELETE(
              "/v1/brands/{brand}/seo-sites/{site}/insights/schedules/{item}",
              { params: { path: { ...path, item: id } } },
            );
      if (error) throw Error(errorMessage(error));
      track("ui.search_workspace_deleted", { kind });
    });
  }
  if (!path.brand)
    return (
      <Empty title="Assign this site to a brand">
        Search work belongs to a brand. Update the site in Audits to continue.
      </Empty>
    );
  const items = data?.items ?? [];
  const active = items.filter((x) => !["done", "dismissed"].includes(x.status));
  const failed =
    data?.activity.filter((x) => ["dead", "failed"].includes(x.status)) ?? [];
  const matches = (tags: string[], text: string) =>
    !tag || tags.includes(tag)
      ? text.toLowerCase().includes(search.toLowerCase())
      : false;
  const work = items.filter(
    (x) =>
      (!status || x.status === status) &&
      matches(x.tags, [x.title, x.page, x.query, x.note, ...x.tags].join(" ")),
  );
  const toolbar = (
    <div className="flex flex-wrap items-end gap-3">
      <label className="text-xs">
        Search
        <input
          className={field}
          placeholder="Find by name, page, note or tag"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </label>
      <label className="text-xs">
        Tag
        <select
          className={field}
          value={tag}
          onChange={(e) => setTag(e.target.value)}
        >
          <option value="">All tags</option>
          {[
            ...new Set(
              (area === "reports" ? (data?.reports ?? []) : items).flatMap(
                (x) => x.tags,
              ),
            ),
          ]
            .sort()
            .map((t) => (
              <option key={t}>{t}</option>
            ))}
        </select>
      </label>
      {area === "work" && (
        <label className="text-xs">
          Status
          <select
            className={field}
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            <option value="">All statuses</option>
            {["open", "in_progress", "done", "dismissed"].map((s) => (
              <option key={s} value={s}>
                {s.replaceAll("_", " ")}
              </option>
            ))}
          </select>
        </label>
      )}
      {(search || tag || status) && (
        <Button
          variant="ghost"
          onClick={() => {
            setSearch("");
            setTag("");
            setStatus("");
          }}
        >
          Clear filters
        </Button>
      )}
    </div>
  );
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">Search workspace</h2>
          <p className="text-sm text-muted-foreground">
            {site.host} · Investigate → prioritise → act → measure
          </p>
        </div>
        <Button variant="ghost" disabled={!!busy} onClick={reload}>
          Refresh workspace
        </Button>
      </div>
      <nav
        aria-label="Search workspace"
        className="flex flex-wrap gap-1 border-b pb-2"
      >
        {(Object.entries(areas) as [Area, string][]).map(([a, label]) => (
          <button
            key={a}
            aria-current={area === a ? "page" : undefined}
            className={`rounded-md px-3 py-2 text-sm ${area === a ? "bg-primary text-primary-foreground" : "hover:bg-muted text-muted-foreground"}`}
            onClick={() => navigate(a)}
          >
            {label}
            {a === "work" && active.length > 0
              ? ` (${active.length})`
              : a === "activity" && failed.length > 0
                ? ` (${failed.length} failed)`
                : ""}
          </button>
        ))}
      </nav>
      {error && <ErrorRecovery message={error} onRetry={reload} />}
      {!data ? (
        error ? null : (
          <Loading />
        )
      ) : (
        <>
          {area === "overview" && (
            <>
              <div className="grid gap-4 sm:grid-cols-3">
                {[
                  ["Open actions", active.length, "work"],
                  ["Saved reports", data?.reports.length ?? 0, "reports"],
                  ["Failed collections", failed.length, "activity"],
                ].map(([title, count, a]) => (
                  <button
                    key={title}
                    className={`${panel} text-left hover:bg-muted/30`}
                    onClick={() => navigate(a as Area)}
                  >
                    <p className="text-sm text-muted-foreground">{title}</p>
                    <p className="text-3xl font-semibold tabular-nums">
                      {count}
                    </p>
                    <p className="text-xs">Review →</p>
                  </button>
                ))}
              </div>
              <Recommendations
                path={path}
                onInspect={inspect}
                onExplore={explore}
              />
              <div className="grid gap-4 lg:grid-cols-2">
                <section className={panel}>
                  <h3 className="font-semibold">What needs attention?</h3>
                  {failed.length > 0 && (
                    <button
                      className="block text-left text-sm text-destructive underline"
                      onClick={() => navigate("activity")}
                    >
                      {failed.length} collections failed. Review errors and
                      retry.
                    </button>
                  )}
                  {active.slice(0, 4).map((x) => (
                    <button
                      key={x.id}
                      className="block w-full rounded border p-3 text-left hover:bg-muted/30"
                      onClick={() =>
                        inspect(x.evidence, { report: x.report }, x)
                      }
                    >
                      <p className="font-medium text-sm">{x.title}</p>
                      <p className="truncate text-xs text-muted-foreground">
                        {x.page || x.query}
                      </p>
                      <Tags tags={x.tags} />
                    </button>
                  ))}
                  {!active.length && (
                    <p className="text-sm text-muted-foreground">
                      Start with opportunities or declining content. Save the
                      findings you want to work on, add tags and turn them into
                      fixes.
                    </p>
                  )}
                  <div className="flex flex-wrap gap-2">
                    <Button
                      onClick={() => explore({ report: "opportunities" })}
                    >
                      Find opportunities
                    </Button>
                    <Button
                      variant="ghost"
                      onClick={() => explore({ report: "decay" })}
                    >
                      Review declines
                    </Button>
                  </div>
                </section>
                <section className={panel}>
                  <h3 className="font-semibold">Data and collection</h3>
                  <p className="text-sm">
                    Search Console:{" "}
                    {data?.search_source ? "Connected" : "Not connected"}
                  </p>
                  {data?.search_source && (
                    <>
                      <p className="text-xs text-muted-foreground">
                        Last sync: {when(data.search_source.last_run_at)}
                      </p>
                      {data.search_source.last_error && (
                        <p role="alert" className="text-sm text-destructive">
                          {data.search_source.last_error}
                        </p>
                      )}
                    </>
                  )}
                  <p className="text-sm text-muted-foreground">
                    Reports use stored observations. Search Console and
                    analytics sync in the background. Core Web Vitals and
                    ranking updates have their own collection schedules.
                  </p>
                  <div className="flex flex-wrap gap-2">
                    <Button variant="ghost" onClick={() => go("connectors")}>
                      Manage connections
                    </Button>
                    <Button
                      variant="ghost"
                      onClick={() => navigate("automations")}
                    >
                      Set up collection
                    </Button>
                    <Button
                      variant="ghost"
                      onClick={() => explore({ report: "completeness" })}
                    >
                      Check completeness
                    </Button>
                  </div>
                </section>
              </div>
              <section className={panel}>
                <h3 className="font-semibold">Choose a task</h3>
                <div className="grid gap-3 md:grid-cols-3">
                  {[
                    [
                      "Grow search traffic",
                      "opportunities",
                      "Find queries close to a better ranking or a stronger snippet.",
                    ],
                    [
                      "Explain a change",
                      "changes",
                      "Compare traffic shifts with deploys and Google updates.",
                    ],
                    [
                      "Measure business value",
                      "attribution",
                      "Connect search landing pages to GA4 outcomes.",
                    ],
                  ].map(([title, report, desc]) => (
                    <button
                      key={report}
                      className="rounded-lg border p-4 text-left hover:bg-muted/30"
                      onClick={() => explore({ report })}
                    >
                      <p className="font-medium">{title} →</p>
                      <p className="mt-2 text-sm text-muted-foreground">
                        {desc}
                      </p>
                    </button>
                  ))}
                </div>
              </section>
            </>
          )}
          {area === "explore" && (
            <div className="space-y-4">
              {editingReport && (
                <p className="rounded border bg-muted p-3 text-sm">
                  Editing filters for <strong>{editingReport.name}</strong>.
                  Select Save report to update its definition. Existing
                  snapshots stay unchanged.
                </p>
              )}
              <SearchExplore
                key={exploreKey}
                ws={ws}
                site={site}
                sites={sites}
                canManage={canManage}
                initial={initial}
                items={items}
                onWorkSaved={reload}
                onInspect={inspect}
                onSaveReport={(filters) =>
                  setReportEditor({ filters, current: editingReport })
                }
              />
            </div>
          )}
          {area === "work" && (
            <>
              <div>
                <h3 className="font-semibold">Your prioritised work</h3>
                <p className="text-sm text-muted-foreground">
                  Saved findings retain their original evidence. Tag, add notes
                  and create a fix to assign and track implementation.
                </p>
              </div>
              {toolbar}
              {!items.length ? (
                <Empty
                  title="Build your action list"
                  action={
                    <Button
                      onClick={() => explore({ report: "opportunities" })}
                    >
                      Explore findings
                    </Button>
                  }
                >
                  Inspect a result and save it to work. You can then organise it
                  without losing the evidence.
                </Empty>
              ) : !work.length ? (
                <Empty title="No work matches these filters">
                  Clear the filters to see your saved work.
                </Empty>
              ) : (
                <div className="space-y-2">
                  {work.map((x) => (
                    <button
                      className={`${panel} block w-full text-left hover:bg-muted/30`}
                      key={x.id}
                      onClick={() =>
                        inspect(x.evidence, { report: x.report }, x)
                      }
                    >
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <h4 className="font-medium">{x.title}</h4>
                        <StateBadge status={x.status} />
                      </div>
                      <p className="break-all text-sm text-muted-foreground">
                        {x.page || x.query}
                      </p>
                      <Tags tags={x.tags} />
                      <p className="text-sm line-clamp-2">
                        {x.note || x.recommendation}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        Updated {when(x.updated_at)} ·{" "}
                        {x.fix_id
                          ? "Linked to Fixes"
                          : "No implementation fix yet"}{" "}
                        · Inspect →
                      </p>
                    </button>
                  ))}
                </div>
              )}
            </>
          )}
          {area === "reports" && (
            <>
              <div className="flex flex-wrap justify-between gap-2">
                <div>
                  <h3 className="font-semibold">Saved reports and snapshots</h3>
                  <p className="text-sm text-muted-foreground">
                    A definition saves your filters. Each run preserves a
                    separate snapshot, including its exact data period.
                  </p>
                </div>
                <Button onClick={() => explore({ report: "opportunities" })}>
                  {canManage ? "Create from Explore" : "Explore reports"}
                </Button>
              </div>
              {toolbar}
              {!data?.reports.length ? (
                <Empty title="Save your first report">
                  Choose a report and filters in Explore, then select Save
                  report.
                </Empty>
              ) : (
                data.reports
                  .filter((p) =>
                    matches(p.tags, p.name + " " + p.filters.report),
                  )
                  .map((p) => (
                    <section key={p.id} className={panel}>
                      <div className="flex flex-wrap justify-between gap-2">
                        <div>
                          <h4 className="font-medium">{p.name}</h4>
                          <p className="text-xs text-muted-foreground">
                            {reports[p.filters.report as Report] ??
                              p.filters.report}{" "}
                            · {p.filters.from || "Latest period"}
                            {p.filters.to ? ` – ${p.filters.to}` : ""}
                          </p>
                        </div>
                        {p.latest_run ? (
                          <StateBadge status={p.latest_run.status} />
                        ) : (
                          <StateBadge status="never_run" />
                        )}
                      </div>
                      <Tags tags={p.tags} />
                      <p className="text-xs text-muted-foreground">
                        Last run: {when(p.latest_run?.created_at)}
                        {p.latest_run?.error ? ` · ${p.latest_run.error}` : ""}
                      </p>
                      <div className="flex flex-wrap gap-2">
                        {canManage && (
                          <Button disabled={!!busy} onClick={() => run(p)}>
                            {busy === p.id
                              ? "Generating…"
                              : "Generate snapshot"}
                          </Button>
                        )}
                        {p.latest_run && (
                          <Button
                            variant="ghost"
                            onClick={() => void openSnapshot(p.latest_run!)}
                          >
                            View latest
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          onClick={() => explore(p.filters, p)}
                        >
                          Edit filters in Explore
                        </Button>
                        {canManage && (
                          <>
                            <Button
                              variant="ghost"
                              onClick={() =>
                                setReportEditor({
                                  current: p,
                                  filters: p.filters,
                                })
                              }
                            >
                              Edit name & tags
                            </Button>
                            <Button
                              variant="ghost"
                              disabled={!!busy}
                              onClick={() => remove("report", p.id)}
                            >
                              Delete
                            </Button>
                          </>
                        )}
                      </div>
                    </section>
                  ))
              )}
              <section className={panel}>
                <h3 className="font-semibold">Recent snapshots</h3>
                {data?.report_runs.length ? (
                  data.report_runs.map((r) => (
                    <button
                      className="flex w-full flex-wrap justify-between gap-2 border-b py-3 text-left last:border-0"
                      key={r.id}
                      onClick={() => void openSnapshot(r)}
                    >
                      <span>
                        {r.report_name}
                        <span className="block text-xs text-muted-foreground">
                          {when(r.created_at)}
                        </span>
                      </span>
                      <StateBadge status={r.status} />
                    </button>
                  ))
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No snapshots generated yet.
                  </p>
                )}
              </section>
            </>
          )}
          {area === "automations" && (
            <>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h3 className="font-semibold">Data collection schedules</h3>
                  <p className="text-sm text-muted-foreground">
                    Choose what to collect and when. These schedules do not
                    modify your website.
                  </p>
                </div>
                {canManage && (
                  <Button onClick={() => setScheduleEditor({})}>
                    New schedule
                  </Button>
                )}
              </div>
              {!data?.schedules.length ? (
                <Empty
                  title="Choose your collection cadence"
                  action={
                    canManage ? (
                      <Button onClick={() => setScheduleEditor({})}>
                        Create a schedule
                      </Button>
                    ) : undefined
                  }
                >
                  Collect official Google updates, or real-user Core Web Vitals
                  with a CrUX connection. Times use your chosen timezone.
                </Empty>
              ) : (
                data.schedules.map((s) => (
                  <section className={panel} key={s.id}>
                    <div className="flex flex-wrap justify-between gap-2">
                      <h4 className="font-medium">{s.name}</h4>
                      <StateBadge
                        status={s.frequency === "off" ? "paused" : "active"}
                      />
                    </div>
                    <p className="text-sm">
                      {s.integration_id
                        ? `Core Web Vitals · ${s.device}`
                        : "Official Google ranking updates"}
                    </p>
                    {s.integration_id && (
                      <p className="break-all text-xs text-muted-foreground">
                        {s.page}
                      </p>
                    )}
                    <p className="text-sm">
                      {s.frequency === "off"
                        ? "Paused"
                        : `${s.frequency === "weekly" ? ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"][s.weekday] : "Every day"} at ${String(s.hour).padStart(2, "0")}:00 · ${s.timezone}`}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Next run:{" "}
                      {s.next_run_at
                        ? when(s.next_run_at)
                        : "None while paused"}
                    </p>
                    {(() => {
                      const last = data.activity.find(
                        (r) => r.schedule_id === s.id,
                      );
                      return last ? (
                        <p className="text-xs">
                          Latest: <StateBadge status={last.status} /> ·{" "}
                          {when(last.updated_at)}
                        </p>
                      ) : (
                        <p className="text-xs text-muted-foreground">
                          No collections run yet.
                        </p>
                      );
                    })()}
                    {canManage && (
                      <div className="flex flex-wrap gap-2">
                        <Button
                          disabled={!!busy}
                          variant="ghost"
                          onClick={() => collect(s)}
                        >
                          Run now
                        </Button>
                        <Button
                          variant="ghost"
                          onClick={() =>
                            setScheduleEditor({
                              current:
                                s.frequency === "off"
                                  ? { ...s, frequency: "weekly" }
                                  : s,
                            })
                          }
                        >
                          Edit
                        </Button>
                        <Button
                          disabled={!!busy}
                          variant="ghost"
                          onClick={() =>
                            s.frequency === "off"
                              ? setScheduleEditor({
                                  current:
                                    s.frequency === "off"
                                      ? { ...s, frequency: "weekly" }
                                      : s,
                                })
                              : pause(s)
                          }
                        >
                          {s.frequency === "off" ? "Resume…" : "Pause"}
                        </Button>
                        <Button
                          disabled={!!busy}
                          variant="ghost"
                          onClick={() => remove("schedule", s.id)}
                        >
                          Delete
                        </Button>
                      </div>
                    )}
                  </section>
                ))
              )}
              <section className={panel}>
                <h3 className="font-semibold">
                  Connect the rest of the workflow
                </h3>
                <p className="text-sm text-muted-foreground">
                  Use Digests for scheduled search work and collection
                  summaries, Fixes for ownership and implementation, and
                  Experiments to measure an intentional change.
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button variant="ghost" onClick={() => go("digests")}>
                    Configure digests
                  </Button>
                  <Button variant="ghost" onClick={() => go("fixes")}>
                    Implementation queue
                  </Button>
                  <Button variant="ghost" onClick={() => go("experiments")}>
                    Measure changes
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => go("connectors", "google_crux")}
                  >
                    Connect CrUX
                  </Button>
                </div>
                {canManage && (
                  <Button
                    disabled={!!busy}
                    variant="ghost"
                    onClick={() => collect()}
                  >
                    Collect Google updates now
                  </Button>
                )}
              </section>
            </>
          )}
          {area === "activity" && (
            <>
              <div className="flex flex-wrap items-end gap-3">
                <div>
                  <h3 className="font-semibold">
                    Collection and report activity
                  </h3>
                  <p className="text-sm text-muted-foreground">
                    Collection may retry automatically. Report runs read stored
                    data and preserve a snapshot.
                  </p>
                </div>
                <label className="text-xs">
                  Show
                  <select
                    className={field}
                    value={status}
                    onChange={(e) => setStatus(e.target.value)}
                  >
                    <option value="">All runs</option>
                    <option value="failed">Failed</option>
                    <option value="active">Queued or running</option>
                    <option value="succeeded">Succeeded</option>
                  </select>
                </label>
              </div>
              {[
                ...(data?.activity ?? []).map((r) => ({
                  ...r,
                  type: "collection" as const,
                })),
                ...(data?.report_runs ?? []).map((r) => ({
                  ...r,
                  type: "report" as const,
                })),
              ]
                .sort((a, b) => b.created_at.localeCompare(a.created_at))
                .filter(
                  (r) =>
                    !status ||
                    (status === "failed"
                      ? ["failed", "dead"].includes(r.status)
                      : status === "active"
                        ? ["queued", "running"].includes(r.status)
                        : r.status === status),
                )
                .map((r) => (
                  <section className={panel} key={r.id}>
                    <div className="flex flex-wrap justify-between gap-2">
                      <h4 className="font-medium">
                        {r.type === "report"
                          ? r.report_name
                          : r.integration_id
                            ? "Core Web Vitals collection"
                            : "Google ranking updates collection"}
                      </h4>
                      <StateBadge status={r.status} />
                    </div>
                    <p className="text-xs text-muted-foreground">
                      Created {when(r.created_at)} ·{" "}
                      {r.type === "report"
                        ? `Completed ${when(r.completed_at)}`
                        : `Updated ${when(r.updated_at)} · Attempt ${r.attempts}/${r.max_attempts}`}
                    </p>
                    {r.type === "collection" && r.status === "queued" && (
                      <p className="text-xs">
                        Scheduled / next retry: {when(r.run_at)}
                      </p>
                    )}
                    {r.error && (
                      <p
                        role="alert"
                        className="whitespace-pre-wrap break-words rounded border border-destructive/30 bg-destructive/5 p-3 text-sm"
                      >
                        {r.error}
                      </p>
                    )}
                    <div className="flex gap-2">
                      {r.type === "report" ? (
                        <>
                          <Button
                            variant="ghost"
                            onClick={() => void openSnapshot(r)}
                          >
                            View snapshot
                          </Button>
                          {canManage && r.status === "failed" && (
                            <Button
                              disabled={!!busy}
                              onClick={() => {
                                const p = data?.reports.find(
                                  (p) => p.id === r.report_id,
                                );
                                if (p) void run(p);
                              }}
                            >
                              Run again
                            </Button>
                          )}
                        </>
                      ) : (
                        canManage &&
                        ["dead", "failed"].includes(r.status) && (
                          <Button
                            disabled={!!busy}
                            onClick={() =>
                              collect({
                                integration_id: r.integration_id,
                                page: r.page,
                                device: r.device,
                              } as Schedule)
                            }
                          >
                            Retry collection
                          </Button>
                        )
                      )}
                    </div>
                  </section>
                ))}
              {!data?.activity.length && !data?.report_runs.length && (
                <Empty title="No runs yet">
                  Generate a saved report or run a collection to see its
                  progress here.
                </Empty>
              )}
            </>
          )}
        </>
      )}
      <Sheet
        open={!!reportEditor}
        onOpenChange={(o) => {
          if (!o) setReportEditor(undefined);
        }}
      >
        <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
          <SheetHeader>
            <SheetTitle>
              {reportEditor?.current ? "Edit report" : "Save report definition"}
            </SheetTitle>
            <SheetDescription>
              Reuse filters and preserve results as snapshots.
            </SheetDescription>
          </SheetHeader>
          <div className="p-4">
            {reportEditor && (
              <ReportEditor
                path={path}
                current={reportEditor.current}
                filters={reportEditor.filters}
                onCancel={() => setReportEditor(undefined)}
                onSaved={() => {
                  track("ui.search_report_saved", {
                    report: reportEditor.filters.report ?? "opportunities",
                  });
                  setReportEditor(undefined);
                  reload();
                  navigate("reports");
                }}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
      <Sheet
        open={!!scheduleEditor}
        onOpenChange={(o) => {
          if (!o) setScheduleEditor(undefined);
        }}
      >
        <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
          <SheetHeader>
            <SheetTitle>
              {scheduleEditor?.current
                ? "Edit collection"
                : "Schedule collection"}
            </SheetTitle>
            <SheetDescription>
              Choose a source and an explicit local schedule.
            </SheetDescription>
          </SheetHeader>
          <div className="p-4">
            {scheduleEditor && (
              <ScheduleEditor
                path={path}
                host={site.host}
                connections={connections}
                current={scheduleEditor.current}
                onCancel={() => setScheduleEditor(undefined)}
                onSaved={() => {
                  track("ui.search_schedule_changed", {});
                  setScheduleEditor(undefined);
                  reload();
                }}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
      <Sheet
        open={!!finding}
        onOpenChange={(o) => {
          if (!o) setFinding(undefined);
        }}
      >
        <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
          <SheetHeader>
            <SheetTitle>
              {finding?.item?.title ??
                (finding
                  ? findingAdvice(finding.filters.report as Report, finding.row)
                      .title
                  : "Finding")}
            </SheetTitle>
            <SheetDescription>
              Review evidence, choose an action and track its outcome.
            </SheetDescription>
          </SheetHeader>
          <div className="p-4">
            {finding && (
              <Finding
                key={`${finding.filters.report}:${finding.row.key}`}
                ws={ws}
                path={path}
                row={finding.row}
                filters={finding.filters}
                item={finding.item}
                canManage={canManage}
                go={go}
                onSaved={(item) => {
                  setFinding({ ...finding, item });
                  reload();
                }}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
      <Sheet
        open={!!snapshot}
        onOpenChange={(o) => {
          if (!o) setSnapshot(undefined);
        }}
      >
        <SheetContent className="w-full overflow-y-auto sm:max-w-5xl">
          <SheetHeader>
            <SheetTitle>{snapshot?.report_name}</SheetTitle>
            <SheetDescription>
              Snapshot generated {when(snapshot?.created_at)}. Earlier snapshots
              never change when the report definition is edited.
            </SheetDescription>
          </SheetHeader>
          <div className="p-4 space-y-4">
            {snapshot && (
              <>
                <StateBadge status={snapshot.status} />
                {busy === "snapshot" && <Loading />}
                {snapshot.error && (
                  <p role="alert" className="text-destructive">
                    {snapshot.error}
                  </p>
                )}
                {snapshot.result && (
                  <>
                    <p className="text-sm">
                      Data period: {snapshot.result.from} – {snapshot.result.to}
                    </p>
                    {snapshot.result.notes.map((n, i) => (
                      <p className="text-xs text-muted-foreground" key={i}>
                        {n}
                      </p>
                    ))}
                    <InsightTable
                      selectable={false}
                      report={snapshot.result.report}
                      rows={snapshot.result.data}
                      selected={new Set()}
                      setSelected={() => {}}
                      onOpen={(row) => {
                        setSnapshot(undefined);
                        inspect(row, {
                          ...snapshot.filters,
                          from: snapshot.result?.from,
                          to: snapshot.result?.to,
                        });
                      }}
                      tagsFor={(key) =>
                        items.find(
                          (x) =>
                            x.report === snapshot.result?.report &&
                            x.key === key,
                        )?.tags ?? []
                      }
                    />
                  </>
                )}
              </>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}
