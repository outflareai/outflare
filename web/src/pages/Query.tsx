import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { EditorView } from "@codemirror/view";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { BulkEditSheet } from "./BulkEdit";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { nameItem, useCatalog } from "@/lib/records";
import { PickBrand } from "@/components/pick-brand";
import { insertAt, SqlEditor, type SqlSchema } from "@/components/sql-editor";
import { defaultChart, numericColumns, QueryChart, type ChartSpec, type ChartType } from "@/components/query-chart";
import { EndpointSheet, EndpointsList, PublishSheet, useEndpoints } from "./QueryEndpoints";

// Query Studio (plans/brands/README.md, "Data pages"): SQL over one brand's
// data. The tables and the brand's record types on the left, an editor
// with autocomplete, results as a table or a chart, CSV download, and
// saved queries with their versions. Anyone with customers.read runs and
// downloads; saving and sharing needs customers.manage.

type Saved = Schemas["SavedQuery"];
type Version = Schemas["SavedQueryVersion"];
type Result = Schemas["QueryResult"];
type Table = Schemas["QueryTable"];

const rowsShown = 1000;
const sameChart = (a: ChartSpec | null | undefined, b: ChartSpec | null | undefined) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

export function Query({ ws, canManage, canPublish = false, open, onOpen }: { ws: string; canManage: boolean; canPublish?: boolean; open: string; onOpen: (item: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("query", "Query"), []);
  if (!brand) {
    return (
      <div className="space-y-6">
        <PageHeader title="Query" />
        <PickBrand what="Queries" brands={active.length} />
      </div>
    );
  }
  return <Studio key={brand.id} ws={ws} brand={brand.id} canManage={canManage} canPublish={canPublish} open={open} onOpen={onOpen} />;
}

function Studio({ ws, brand, canManage, canPublish, open, onOpen }: { ws: string; brand: string; canManage: boolean; canPublish: boolean; open: string; onOpen: (item: string) => void }) {
  const endpoints = useEndpoints(brand);
  const [publishing, setPublishing] = useState(false);
  const [endpoint, setEndpoint] = useState<Schemas["QueryEndpoint"] | null>(null);
  const [tables, setTables] = useState<Table[]>([]);
  const [saved, setSaved] = useState<Saved[] | null>(null);
  const [current, setCurrent] = useState<Saved | null>(null);
  const [bulk, setBulk] = useState(false);
  const [sql, setSQL] = useState("");
  const [viewing, setViewing] = useState<Version | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const [ranSQL, setRanSQL] = useState("");
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  const [view, setView] = useState<"table" | "chart">("table");
  const [chart, setChart] = useState<ChartSpec | null>(null);
  const [showVersions, setShowVersions] = useState(false);
  const editor = useRef<EditorView | null>(null);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/query/tables", { params: { path: { brand } } }).then(({ data, error }) => {
      if (data) setTables(data.data);
      else setError(errorMessage(error));
    });
  }, [brand]);

  const loadSaved = useCallback(() => {
    api.GET("/v1/brands/{brand}/queries", { params: { path: { brand } } }).then(({ data }) => setSaved(data?.data ?? []));
  }, [brand]);
  useEffect(loadSaved, [loadSaved]);

  // A saved query opened by its link (/query/<id>).
  useEffect(() => {
    if (!open) {
      setCurrent(null);
      return;
    }
    if (current?.id === open) return;
    api.GET("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand, saved_query: open } } }).then(({ data, error }) => {
      if (!data) return setError(errorMessage(error));
      openSaved(data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, brand]);

  function openSaved(q: Saved) {
    setCurrent(q);
    setSQL(q.sql);
    setChart(q.chart ?? null);
    setView(q.chart ? "chart" : "table");
    setViewing(null);
    setResult(null);
    setError("");
    nameItem(q.id, q.name);
  }

  const schema = useMemo<SqlSchema>(() => Object.fromEntries(tables.filter((t) => t.available).map((t) => [t.name, t.columns.map((c) => c.name)])), [tables]);
  const dirty = current ? sql.trim() !== current.sql.trim() || !sameChart(chart, current.chart) : sql.trim() !== "";
  const sqlDirty = current ? sql.trim() !== current.sql.trim() : true;

  async function run() {
    const text = sql.trim();
    if (!text || running) return;
    setRunning(true);
    setError("");
    // A saved query unchanged runs as itself, so its runs are counted.
    const res = current && !sqlDirty && !viewing
      ? await api.POST("/v1/brands/{brand}/queries/{saved_query}/run", { params: { path: { brand, saved_query: current.id } }, body: { limit: rowsShown } })
      : await api.POST("/v1/brands/{brand}/query", { params: { path: { brand } }, body: { sql: text, limit: rowsShown } });
    setRunning(false);
    if (!res.data) {
      setResult(null);
      return setError(errorMessage(res.error));
    }
    setResult(res.data);
    setRanSQL(text);
    if (!chart) setChart(defaultChart(res.data.columns, res.data.rows));
  }

  async function download() {
    const text = sql.trim();
    if (!text) return;
    const res = current && !sqlDirty && !viewing
      ? await api.POST("/v1/brands/{brand}/queries/{saved_query}/csv", { params: { path: { brand, saved_query: current.id } }, body: {}, parseAs: "blob" })
      : await api.POST("/v1/brands/{brand}/query/csv", { params: { path: { brand } }, body: { sql: text }, parseAs: "blob" });
    if (res.error || !(res.data instanceof Blob)) return toast(errorMessage(res.error), "error");
    const name = /filename="([^"]+)"/.exec(res.response.headers.get("Content-Disposition") ?? "")?.[1] ?? "outflare-query.csv";
    const url = URL.createObjectURL(res.data);
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
    const rows = Number(res.response.headers.get("Outflare-Query-Rows") ?? 0);
    toast(res.response.headers.get("Outflare-Query-Truncated") === "true" ? `Downloaded the first ${plural(rows, "row")}: the query has more.` : `Downloaded ${plural(rows, "row")}.`);
  }

  async function saveAs() {
    const r = await ask({
      title: "Save query",
      body: "Saved queries are private to you until you share them with the brand.",
      input: { label: "Name", maxLength: 200, value: current ? `${current.name} (copy)` : "" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "save", variant: "primary" }],
    });
    if (!r?.value.trim()) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/queries", { params: { path: { brand } }, body: { name: r.value.trim(), sql: sql.trim(), chart } });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast("Query saved.");
    openSaved(data);
    onOpen(data.id);
    loadSaved();
  }

  async function save() {
    if (!current || !current.can_edit) return saveAs();
    const r = sqlDirty
      ? await ask({
          title: `Save version ${current.current_version + 1}`,
          body: "The current version stays in the history, where it can be restored.",
          input: { label: "What changed (optional)", maxLength: 500, optional: true },
          choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "save", variant: "primary" }],
        })
      : { choice: "save", value: "" };
    if (!r) return;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/queries/{saved_query}", {
      params: { path: { brand, saved_query: current.id } },
      body: { sql: sql.trim(), chart, ...(r.value.trim() ? { note: r.value.trim() } : {}) },
    });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast(data.current_version > current.current_version ? `Saved as version ${data.current_version}.` : "Saved.");
    openSaved(data);
    loadSaved();
  }

  async function patch(body: { name?: string; description?: string; visibility?: "private" | "brand" }, done: string) {
    if (!current) return;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand, saved_query: current.id } }, body });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast(done);
    setCurrent(data);
    nameItem(data.id, data.name);
    loadSaved();
  }

  async function rename() {
    if (!current) return;
    const r = await ask({ title: "Rename query", input: { label: "Name", maxLength: 200, value: current.name }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Rename", value: "ok", variant: "primary" }] });
    if (r?.value.trim()) patch({ name: r.value.trim() }, "Renamed.");
  }

  async function archive() {
    if (!current) return;
    const ok = await confirmAction("Archive this query?", <p>It leaves the list for everyone it is shared with, and can no longer be run or changed. Its versions are kept.</p>, "Archive");
    if (!ok) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/queries/{saved_query}/archive", { params: { path: { brand, saved_query: current.id } } });
    if (!data) return toast(errorMessage(error), "error");
    toast("Query archived.");
    setCurrent(null);
    onOpen("");
    loadSaved();
  }

  function newQuery() {
    setCurrent(null);
    setSQL("");
    setChart(null);
    setResult(null);
    setViewing(null);
    setError("");
    onOpen("");
  }

  function insert(text: string, kind: "table" | "column" | "type") {
    insertAt(editor.current, text);
    track("ui.query_table_inserted", { kind });
  }

  const readOnlyEditor = !!viewing;

  return (
    <div className="space-y-4">
      <PageHeader
        title={current ? current.name : "Query"}
        description={
          current ? (
            <span className="flex flex-wrap items-center gap-2">
              <Badge tone={current.visibility === "brand" ? "blue" : "grey"}>{current.visibility === "brand" ? "Shared with the brand" : "Private"}</Badge>
              <span>Version {current.current_version} · changed <span title={stamp(current.updated_at)}>{ago(current.updated_at)}</span></span>
              {current.archived_at && <Badge tone="amber">Archived</Badge>}
            </span>
          ) : (
            "One SELECT over this brand's data. Every table holds this brand's rows only, and you see what your role may read."
          )
        }
        action={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" onClick={newQuery}>New query</Button>
            {current && (
              <Button variant="ghost" onClick={() => { setShowVersions(!showVersions); if (!showVersions) track("ui.query_versions_opened", { versions: current.current_version }); }}>
                {showVersions ? "Hide versions" : "Versions"}
              </Button>
            )}
            {canManage && current?.can_edit && (
              <Menu
                items={[
                  { label: "Rename", onClick: rename },
                  current.mine && { label: current.visibility === "brand" ? "Make private" : "Share with the brand", onClick: () => patch({ visibility: current.visibility === "brand" ? "private" : "brand" }, current.visibility === "brand" ? "Only you see it now." : "Shared with everyone who can query this brand.") },
                  { label: "Save as new query", onClick: saveAs },
                  canPublish && !current.archived_at && { label: "Publish as an endpoint", onClick: () => setPublishing(true) },
                  !current.archived_at && !dirty && { label: "Bulk edit its records", onClick: () => setBulk(true) },
                  { label: "Archive", onClick: archive, danger: true },
                ]}
              />
            )}
          </div>
        }
      />
      {bulk && current && <BulkEditSheet ws={ws} brand={brand} source={{ kind: "query", id: current.id, name: current.name }} onClose={() => setBulk(false)} onDone={() => setBulk(false)} />}
      {canManage === false && <p className="text-[12px] text-muted-foreground">You can run queries and download their results. Saving and sharing queries needs the customers.manage permission.</p>}
      <div className="grid gap-4 lg:grid-cols-[260px_minmax(0,1fr)]">
        <aside className="space-y-4">
          <SavedList saved={saved} current={current?.id} onPick={(q) => { openSaved(q); onOpen(q.id); track("ui.query_saved_opened", { shared: q.visibility === "brand", mine: q.mine }); }} />
          <EndpointsList list={endpoints.list} onPick={setEndpoint} />
          <Tables tables={tables} onInsert={insert} />
          <CatalogTypes ws={ws} brand={brand} tables={tables} onInsert={insert} />
        </aside>
        <main className="min-w-0 space-y-3">
          {viewing && (
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-amber-600/30 bg-amber-500/10 px-3 py-2 text-[13px]">
              <span className="flex-1">Viewing version {viewing.version}{viewing.note ? `: ${viewing.note}` : ""}. Restore it to make it current, or go back.</span>
              <Button variant="ghost" onClick={() => { setViewing(null); if (current) { setSQL(current.sql); setChart(current.chart ?? null); } }}>Back to current</Button>
            </div>
          )}
          <SqlEditor value={sql} onChange={setSQL} onRun={run} schema={schema} editor={editor} readOnly={readOnlyEditor} label="SQL" />
          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={run} disabled={running || !sql.trim()}>{running ? "Running…" : "Run"}</Button>
            <span className="text-[12px] text-muted-foreground">⌘/Ctrl + Enter</span>
            <div className="flex-1" />
            <Button variant="ghost" onClick={download} disabled={!sql.trim()}>Download CSV</Button>
            {canManage && !viewing && (
              current?.can_edit ? (
                <Button onClick={save} disabled={!dirty}>Save</Button>
              ) : (
                <Button variant={current ? "ghost" : "primary"} onClick={saveAs} disabled={!sql.trim()}>Save query</Button>
              )
            )}
          </div>
          {error && <ErrorText>{error}</ErrorText>}
          {result && (
            <Results result={result} stale={ranSQL !== sql.trim()} view={view} setView={setView} chart={chart} setChart={setChart} />
          )}
          {current && showVersions && (
            <Versions brand={brand} query={current} canRestore={canManage && current.can_edit} onView={(v) => { setViewing(v); setSQL(v.sql); setChart(v.chart ?? null); }} onRestored={(q) => { openSaved(q); loadSaved(); }} />
          )}
        </main>
      </div>
      {publishing && current && (
        <PublishSheet brand={brand} query={current} onClose={() => setPublishing(false)} onDone={(e) => { setPublishing(false); endpoints.load(); setEndpoint(e); }} />
      )}
      {endpoint && <EndpointSheet brand={brand} endpoint={endpoint} canPublish={canPublish} onClose={() => setEndpoint(null)} onChanged={endpoints.load} />}
    </div>
  );
}

function SavedList({ saved, current, onPick }: { saved: Saved[] | null; current?: string; onPick: (q: Saved) => void }) {
  const [q, setQ] = useState("");
  const shown = (saved ?? []).filter((s) => !q || (s.name + " " + s.description).toLowerCase().includes(q.toLowerCase()));
  const mine = shown.filter((s) => s.mine);
  const shared = shown.filter((s) => !s.mine);
  const item = (s: Saved) => (
    <li key={s.id}>
      <button className={`w-full truncate rounded px-2 py-1 text-left text-[13px] hover:bg-accent ${s.id === current ? "bg-accent font-medium" : ""}`} onClick={() => onPick(s)} title={s.description || s.name}>
        {s.name}
        {s.mine && s.visibility === "brand" && <span className="ml-1 text-[11px] text-muted-foreground">shared</span>}
      </button>
    </li>
  );
  return (
    <section aria-label="Saved queries" className="rounded-lg border bg-card p-3 shadow-xs">
      <h2 className="mb-2 text-[13px] font-medium">Saved queries</h2>
      <input aria-label="Search saved queries" placeholder="Search" className={`${field} mb-2 w-full`} value={q} onChange={(e) => setQ(e.target.value)} />
      {saved === null ? (
        <Loading />
      ) : saved.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">None yet. Run a query, then save it to come back to it or share it with the brand.</p>
      ) : (
        <div className="max-h-64 space-y-2 overflow-auto">
          {mine.length > 0 && (
            <div>
              <div className="px-2 text-[11px] uppercase tracking-wide text-muted-foreground">Mine</div>
              <ul>{mine.map(item)}</ul>
            </div>
          )}
          {shared.length > 0 && (
            <div>
              <div className="px-2 text-[11px] uppercase tracking-wide text-muted-foreground">Shared with the brand</div>
              <ul>{shared.map(item)}</ul>
            </div>
          )}
          {shown.length === 0 && <p className="px-2 text-[12px] text-muted-foreground">No query matches.</p>}
        </div>
      )}
    </section>
  );
}

function Tables({ tables, onInsert }: { tables: Table[]; onInsert: (text: string, kind: "table" | "column") => void }) {
  const [q, setQ] = useState("");
  const [openTable, setOpenTable] = useState("");
  const needle = q.toLowerCase();
  const shown = tables.filter((t) => !needle || t.name.includes(needle) || t.columns.some((c) => c.name.includes(needle)));
  return (
    <section aria-label="Tables" className="rounded-lg border bg-card p-3 shadow-xs">
      <h2 className="mb-2 text-[13px] font-medium">Tables</h2>
      <input aria-label="Search tables and columns" placeholder="Search tables and columns" className={`${field} mb-2 w-full`} value={q} onChange={(e) => setQ(e.target.value)} />
      <ul className="max-h-96 overflow-auto font-mono text-[12px]">
        {shown.map((t) => {
          const expanded = openTable === t.name || (needle !== "" && !t.name.includes(needle));
          return (
            <li key={t.name} className={t.available ? "" : "opacity-50"}>
              <div className="flex items-center gap-1">
                <button className="w-4 text-muted-foreground" aria-label={`${expanded ? "Hide" : "Show"} ${t.name}'s columns`} aria-expanded={expanded} onClick={() => setOpenTable(expanded ? "" : t.name)}>{expanded ? "▾" : "▸"}</button>
                <button className="truncate text-left hover:underline" title={t.available ? `${t.description} Click to insert.` : `Needs ${t.scope}.`} disabled={!t.available} onClick={() => onInsert(t.name, "table")}>{t.name}</button>
              </div>
              {expanded && (
                <ul className="mb-1 ml-5 border-l pl-2">
                  {t.columns.filter((c) => !needle || t.name.includes(needle) || c.name.includes(needle)).map((c) => (
                    <li key={c.name} className="flex justify-between gap-2">
                      <button className="truncate text-left hover:underline" disabled={!t.available} onClick={() => onInsert(c.name, "column")}>{c.name}</button>
                      <span className="shrink-0 text-muted-foreground">{shortType(c.type)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

const shortType = (t: string) => t.replace("timestamp with time zone", "timestamptz").replace("character varying", "varchar").replace("double precision", "float8");

// The brand's catalog: each record type is a table of its own, and each of
// its fields a column, so the types people made show up where data people
// look.
function CatalogTypes({ ws, brand, tables, onInsert }: { ws: string; brand: string; tables: Table[]; onInsert: (text: string, kind: "type" | "column") => void }) {
  const cat = useCatalog(ws, brand);
  const types = (cat.catalog?.types ?? []).filter((t) => !t.archived);
  if (types.length === 0) return null;
  return (
    <section aria-label="Record types" className="rounded-lg border bg-card p-3 shadow-xs">
      <h2 className="mb-1 text-[13px] font-medium">Record types</h2>
      <p className="mb-2 text-[12px] text-muted-foreground">The brand's catalog. Each type is a table, with a column per field.</p>
      <ul className="max-h-72 space-y-1.5 overflow-auto text-[12px]">
        {types.map((t) => {
          const table = t.key === "person" ? "people" : t.key === "account" ? "companies" : t.key;
          const queryable = tables.some((q) => q.name === table);
          return (
            <li key={t.key}>
              <button className="font-medium hover:underline disabled:no-underline" disabled={!queryable} onClick={() => onInsert(table, "type")} title={queryable ? `Insert ${table}` : "Not a table of its own"}>
                {t.plural} <span className="font-mono font-normal text-muted-foreground">{table}</span>
              </button>
              <div className="flex flex-wrap gap-x-2 font-mono text-muted-foreground">
                {t.fields.filter((f) => !f.archived).map((f) => (
                  <button key={f.key} className="hover:text-foreground hover:underline" title={`${f.label} (${f.value_type})`} onClick={() => onInsert(table === t.key ? f.key : `attributes->>'${f.key}'`, "column")}>{f.key}</button>
                ))}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function cell(v: unknown) {
  if (v === null || v === undefined) return <span className="text-muted-foreground">null</span>;
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function Results({ result, stale, view, setView, chart, setChart }: {
  result: Result; stale: boolean; view: "table" | "chart"; setView: (v: "table" | "chart") => void; chart: ChartSpec | null; setChart: (c: ChartSpec | null) => void;
}) {
  const nums = numericColumns(result.columns, result.rows);
  const spec = chart ?? defaultChart(result.columns, result.rows);
  function show(v: "table" | "chart") {
    setView(v);
    if (v === "chart") {
      if (!chart && spec) setChart(spec);
      track("ui.query_chart_shown", { type: spec?.type ?? "none", series: spec?.y.length ?? 0, rows: result.row_count });
    }
  }
  return (
    <section aria-label="Results" className="rounded-lg border bg-card shadow-xs">
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2 text-[12px] text-muted-foreground">
        <span>{plural(result.row_count, "row")} in {result.duration_ms} ms</span>
        {result.truncated && <Badge tone="amber" title="Narrow the query, or download the CSV for up to 10,000 rows.">More rows than shown</Badge>}
        {stale && <span>· the SQL changed since this ran</span>}
        <div className="flex-1" />
        <div role="tablist" className="inline-flex rounded-md border p-0.5">
          {(["table", "chart"] as const).map((v) => (
            <button key={v} role="tab" aria-selected={view === v} className={`rounded px-2 py-0.5 ${view === v ? "bg-accent text-foreground" : ""}`} onClick={() => show(v)}>{v === "table" ? "Table" : "Chart"}</button>
          ))}
        </div>
      </div>
      {view === "table" ? (
        result.columns.length === 0 ? (
          <Empty title="No columns" />
        ) : (
          <div className="max-h-[60vh] overflow-auto">
            <table className="w-full border-collapse font-mono text-[12px]">
              <thead className="sticky top-0 bg-card">
                <tr>
                  {result.columns.map((c, i) => (
                    <th key={i} className="border-b px-3 py-1.5 text-left font-medium whitespace-nowrap" title={c.type}>{c.name}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {result.rows.map((r, i) => (
                  <tr key={i} className="hover:bg-accent/50">
                    {r.map((v, j) => (
                      <td key={j} className={`max-w-96 truncate border-b px-3 py-1 ${typeof v === "number" ? "text-right" : ""}`}>{cell(v)}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
            {result.rows.length === 0 && <p className="p-4 text-[13px] text-muted-foreground">No rows.</p>}
          </div>
        )
      ) : spec ? (
        <div className="space-y-3 p-3">
          <ChartControls columns={result.columns.map((c) => c.name)} nums={nums} spec={spec} onChange={setChart} />
          <QueryChart columns={result.columns} rows={result.rows} spec={spec} />
        </div>
      ) : (
        <p className="p-4 text-[13px] text-muted-foreground">A chart needs a number column, such as count(*).</p>
      )}
    </section>
  );
}

function ChartControls({ columns, nums, spec, onChange }: { columns: string[]; nums: string[]; spec: ChartSpec; onChange: (c: ChartSpec) => void }) {
  return (
    <div className="flex flex-wrap items-center gap-3 text-[12px]">
      <label className="flex items-center gap-1.5">
        Chart
        <select className={field} value={spec.type} onChange={(e) => onChange({ ...spec, type: e.target.value as ChartType })}>
          <option value="bar">Bars</option>
          <option value="line">Lines</option>
          <option value="area">Areas</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5">
        Along the bottom
        <select className={field} value={spec.x} onChange={(e) => onChange({ ...spec, x: e.target.value, y: spec.y.filter((y) => y !== e.target.value) })}>
          {columns.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
      </label>
      <fieldset className="flex flex-wrap items-center gap-2">
        <legend className="sr-only">Numbers to draw</legend>
        {nums.filter((n) => n !== spec.x).map((n) => (
          <label key={n} className="flex items-center gap-1">
            <input
              type="checkbox"
              checked={spec.y.includes(n)}
              disabled={!spec.y.includes(n) && spec.y.length >= 8}
              onChange={(e) => {
                const y = e.target.checked ? [...spec.y, n] : spec.y.filter((x) => x !== n);
                if (y.length > 0) onChange({ ...spec, y });
              }}
            />
            {n}
          </label>
        ))}
      </fieldset>
    </div>
  );
}

function Versions({ brand, query, canRestore, onView, onRestored }: { brand: string; query: Saved; canRestore: boolean; onView: (v: Version) => void; onRestored: (q: Saved) => void }) {
  const [list, setList] = useState<Version[] | null>(null);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/queries/{saved_query}/versions", { params: { path: { brand, saved_query: query.id } } }).then(({ data }) => setList(data?.data ?? []));
  }, [brand, query.id, query.current_version]);

  async function restore(v: Version) {
    const ok = await confirmAction(`Restore version ${v.version}?`, <p>Its SQL and chart become version {query.current_version + 1}. Nothing in the history is lost.</p>, "Restore", false);
    if (!ok) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/queries/{saved_query}/versions/{version}/restore", { params: { path: { brand, saved_query: query.id, version: v.version } } });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast(`Version ${v.version} restored as version ${data.current_version}.`);
    onRestored(data);
  }

  return (
    <section aria-label="Versions" className="rounded-lg border bg-card p-3 shadow-xs">
      <h2 className="mb-2 text-[13px] font-medium">Versions</h2>
      {list === null ? (
        <Loading />
      ) : (
        <ul className="divide-y text-[13px]">
          {list.map((v) => (
            <li key={v.version} className="flex flex-wrap items-center gap-2 py-1.5">
              <span className="font-medium">Version {v.version}</span>
              {v.current && <Badge tone="green">Current</Badge>}
              <span className="text-muted-foreground" title={stamp(v.created_at)}>{ago(v.created_at)}</span>
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{v.note || (v.restored_from ? `Restored version ${v.restored_from}` : "")}</span>
              {!v.current && <Button variant="ghost" onClick={() => { onView(v); track("ui.query_version_viewed", { version: v.version, current: query.current_version }); }}>View</Button>}
              {!v.current && canRestore && <Button variant="ghost" onClick={() => restore(v)}>Restore</Button>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
