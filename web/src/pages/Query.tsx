import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { EditorView } from "@codemirror/view";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { BulkEditSheet } from "./BulkEdit";
import { ask, Badge, Button, confirmAction, CopyButton, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { appPath } from "@/lib/paths";
import { nameItem, useCatalog } from "@/lib/records";
import { cn } from "@/lib/utils";
import { PickBrand } from "@/components/pick-brand";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { insertAt, SqlEditor, type SqlSchema } from "@/components/sql-editor";
import { defaultChart, numericColumns, QueryChart, type ChartSpec, type ChartType } from "@/components/query-chart";
import { BuilderEditor, builderSummary, emptyBuilder, type Builder, type BuilderChoices } from "@/components/query-builder";
import { Icon } from "@/icons";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PublishPage } from "./QueryEndpoints";
import type { Tab } from "../App";

// Queries (docs/query.md), in the Data menu: describe a list or a question
// in plain English, and the workspace's own AI model builds it as a visual
// builder (or SQL, when a builder can't say it), which anyone can change
// without SQL. The list of saved queries comes first; each query opens on a
// page of its own (/query/<id>, a new one at /query/new), with explicit
// Save and Cancel. Anyone with customers.read describes, builds, runs and
// downloads; saving and sharing needs customers.manage.

type Saved = Schemas["SavedQuery"];
type Version = Schemas["SavedQueryVersion"];
type Result = Schemas["QueryResult"];
type Table = Schemas["QueryTable"];
type Question = Schemas["QueryQuestion"];

const rowsShown = 1000;
const isID = (s: string) => /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(s);
const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

/** A query the list page built from a description, handed to the new query's page. */
type Draft = { name: string; question: string; builder: Builder | null; sql: string; explanation: string };
let pendingDraft: Draft | null = null;

const examples = [
  "Customers who spent over $500 in total: their email, user id, sign-up date and lifetime spend, biggest spenders first",
  "People who signed up in the last 30 days and haven't paid yet",
  "Companies with the most open deals",
  "How many people signed up each week this quarter",
];

export function Query({ ws, canManage, canPublish = false, open, onOpen, go }: { ws: string; canManage: boolean; canPublish?: boolean; open: string; onOpen: (item: string) => void; go: (t: Tab, item?: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("query", "Queries"), []);
  if (!brand) {
    return (
      <div className="space-y-6">
        <PageHeader title="Queries" />
        <PickBrand what="Queries" brands={active.length} />
      </div>
    );
  }
  const [id, sub] = open.split("/");
  if (isID(id) && sub === "publish") {
    if (!canPublish) return <Empty title="Only owners and admins publish endpoints" />;
    return <PublishPage key={id} ws={ws} brand={brand.id} query={id} onBack={() => onOpen(id)} onDone={(e) => go("query-endpoints", e.id)} />;
  }
  if (open === "new" || isID(id)) {
    return <Editor key={`${brand.id}:${open}`} ws={ws} brand={brand.id} currency={brand.currency} id={isID(id) ? id : ""} canManage={canManage} canPublish={canPublish} onOpen={onOpen} go={go} />;
  }
  return <QueryList key={brand.id} ws={ws} brand={brand.id} canManage={canManage} onOpen={onOpen} go={go} />;
}

// ---- Describe it in English ----

// useAsk sends a description and waits for the model's answer.
function useAsk(brand: string) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const cancelled = useRef(false);
  useEffect(() => () => { cancelled.current = true; }, []);
  async function run(question: string, base?: { sql?: string; builder?: Builder | null }): Promise<Question | null> {
    setBusy(true);
    setError("");
    setNote("");
    const started = Date.now();
    const { data, error } = await api.POST("/v1/brands/{brand}/query/ask", {
      params: { path: { brand } },
      body: { question, ...(base?.builder ? { builder: base.builder } : base?.sql ? { sql: base.sql } : {}) },
    });
    if (!data || !("id" in data)) {
      setBusy(false);
      setError(errorMessage(error));
      return null;
    }
    let q: Question = data;
    while ((q.status === "queued" || q.status === "running") && !cancelled.current && Date.now() - started < 180_000) {
      await new Promise((r) => setTimeout(r, 1500));
      const res = await api.GET("/v1/brands/{brand}/query/ask/{question}", { params: { path: { brand, question: q.id } } });
      if (!res.data) {
        setBusy(false);
        setError(errorMessage(res.error));
        return null;
      }
      q = res.data;
    }
    setBusy(false);
    track("ui.query_described", { outcome: q.status === "ready" ? (q.kind || "none") : q.status, refines: !!base, seconds: Math.round((Date.now() - started) / 1000) });
    if (q.status === "failed") {
      setError(q.error);
      return null;
    }
    if (q.status !== "ready") {
      setError("It's taking longer than usual. Try again in a minute.");
      return null;
    }
    if (!q.kind) {
      setNote(q.explanation);
      return null;
    }
    return q;
  }
  return { run, busy, error, note };
}

function DescribeBox({ value, onChange, onSubmit, busy, submitLabel, placeholder, error, note, aiLink }: {
  value: string; onChange: (v: string) => void; onSubmit: () => void; busy: boolean; submitLabel: string; placeholder: string; error: string; note: string; aiLink: () => void;
}) {
  const needsAI = /AI model|Connectors|connect/i.test(error);
  return (
    <div className="space-y-2">
      <textarea
        aria-label="Describe the query in plain English"
        className={cn(field, "min-h-16 w-full resize-y py-2 text-[14px] leading-relaxed")}
        placeholder={placeholder}
        maxLength={2000}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && value.trim() && !busy) onSubmit();
        }}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={onSubmit} disabled={busy || !value.trim()}>{busy ? "Building…" : submitLabel}</Button>
        <span className="text-[12px] text-muted-foreground">
          {busy ? "Your AI model is reading your tables and fields. This takes 5 to 30 seconds." : "⌘/Ctrl + Enter. Uses your own AI model; it sees table and field names, never your customers' data."}
        </span>
      </div>
      {error && (
        <ErrorText>
          {error}{" "}
          {needsAI && <button type="button" className="underline" onClick={aiLink}>Set up an AI model</button>}
        </ErrorText>
      )}
      {note && <p className="rounded-md border border-amber-600/30 bg-amber-500/10 px-3 py-2 text-[13px]">{note}</p>}
    </div>
  );
}

// ---- The list ----

function QueryList({ ws, brand, canManage, onOpen, go }: { ws: string; brand: string; canManage: boolean; onOpen: (item: string) => void; go: (t: Tab, item?: string) => void }) {
  const [saved, setSaved] = useState<Saved[] | null>(null);
  const [show, setShow] = useState<"all" | "mine" | "shared">("all");
  const [text, setText] = useState("");
  const asking = useAsk(brand);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/queries", { params: { path: { brand } } }).then(({ data }) => setSaved(data?.data ?? []));
  }, [brand]);

  async function describe() {
    const q = await asking.run(text.trim());
    if (!q) return;
    pendingDraft = { name: q.name, question: q.question, builder: (q.builder as Builder | null) ?? null, sql: q.sql, explanation: q.explanation };
    onOpen("new");
  }

  const rows = (saved ?? []).filter((s) => show === "all" || (show === "mine" ? s.mine : s.visibility === "brand"));
  return (
    <div className="space-y-6">
      <PageHeader
        title="Queries"
        description="Describe a list or a question in plain English. Outflare builds it as a table you can change without SQL, and you can save it, share it, make it a list, chart it or download it."
        action={
          <>
            <Button variant="ghost" onClick={() => { pendingDraft = { name: "", question: "", builder: null, sql: "", explanation: "" }; onOpen("new"); }}>Write SQL</Button>
            <Button onClick={() => { pendingDraft = null; onOpen("new"); }}>New query</Button>
          </>
        }
      />
      <section aria-label="Describe a query" className="space-y-3 rounded-lg border bg-card p-4 shadow-xs">
        <div>
          <h2 className="text-[15px] font-medium">What do you want to see?</h2>
          <p className="text-[13px] text-muted-foreground">Say who or what, which details to show, and how to order them.</p>
        </div>
        <DescribeBox value={text} onChange={setText} onSubmit={describe} busy={asking.busy} submitLabel="Build it" placeholder="Describe the people, companies or numbers you want, which details to show and how to order them." error={asking.error} note={asking.note} aiLink={() => go("ai")} />
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[12px] text-muted-foreground">Try:</span>
          {examples.map((e) => (
            <button key={e} type="button" className="rounded-full border px-2.5 py-0.5 text-left text-[12px] text-muted-foreground hover:bg-accent hover:text-foreground" onClick={() => setText(e)}>{e}</button>
          ))}
        </div>
      </section>
      <section aria-label="Saved queries" className="space-y-2">
        <div className="flex flex-wrap items-center gap-3">
          <h2 className="text-[15px] font-medium">Saved queries</h2>
          <Tabs value={show} onValueChange={(v) => setShow(v as typeof show)}>
            <TabsList className="h-8">
              <TabsTrigger value="all" className="px-3 text-[12px]">All</TabsTrigger>
              <TabsTrigger value="mine" className="px-3 text-[12px]">Mine</TabsTrigger>
              <TabsTrigger value="shared" className="px-3 text-[12px]">Shared with the brand</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
        {!canManage && <p className="text-[12px] text-muted-foreground">You can describe, build, run and download queries. Saving and sharing them needs the customers.manage permission.</p>}
        {saved === null ? (
          <Loading />
        ) : (
          <DataTable<Saved>
            data={rows}
            rowId={(s) => s.id}
            onRowClick={(s) => { track("ui.query_saved_opened", { shared: s.visibility === "brand", mine: s.mine }); onOpen(s.id); }}
            searchPlaceholder="Search saved queries"
            empty={saved.length === 0 ? "No saved queries yet. Describe one above, then save it to come back to it or share it." : "No query matches."}
            initialSort={[{ id: "updated", desc: true }]}
            mobile={(s) => (
              <div className="space-y-0.5">
                <div className="font-medium">{s.name}</div>
                <div className="text-[12px] text-muted-foreground">{s.builder ? "Builder" : "SQL"} · {s.visibility === "brand" ? "Shared" : "Private"} · changed {ago(s.updated_at)}</div>
              </div>
            )}
            columns={[
              {
                id: "name",
                header: "Name",
                accessorFn: (s) => `${s.name} ${s.description} ${s.question}`,
                cell: ({ row: { original: s } }) => (
                  <a href={appPath(ws, "query", s.id)} onClick={(e) => { e.preventDefault(); }} className="block max-w-xl">
                    <span className="font-medium">{s.name}</span>
                    {(s.description || s.question) && <span className="block truncate text-[12px] text-muted-foreground">{s.description || s.question}</span>}
                  </a>
                ),
              },
              {
                id: "made",
                header: "Made with",
                accessorFn: (s) => (s.builder ? "Builder" : "SQL"),
                cell: ({ row: { original: s } }) => <Badge tone={s.builder ? "blue" : "grey"}>{s.builder ? "Builder" : "SQL"}</Badge>,
              },
              {
                id: "shared",
                header: "Who sees it",
                accessorFn: (s) => (s.visibility === "brand" ? "Shared with the brand" : "Only you"),
                cell: ({ row: { original: s } }) => (
                  <span className="flex items-center gap-1.5">
                    <span className={cn("size-2 rounded-full", s.visibility === "brand" ? "bg-emerald-500" : "bg-muted-foreground/50")} aria-hidden />
                    {s.visibility === "brand" ? "Shared with the brand" : "Only you"}
                  </span>
                ),
              },
              { id: "version", header: "Version", accessorFn: (s) => s.current_version, meta: { align: "right" } },
              {
                id: "updated",
                header: "Changed",
                accessorFn: (s) => s.updated_at,
                cell: ({ row: { original: s } }) => <span title={stamp(s.updated_at)}>{ago(s.updated_at)}</span>,
              },
            ]}
          />
        )}
      </section>
    </div>
  );
}

// ---- One query ----

function useChoices(ws: string, brand: string) {
  const [tables, setTables] = useState<Table[]>([]);
  const [error, setError] = useState("");
  const cat = useCatalog(ws, brand);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/query/tables", { params: { path: { brand } } }).then(({ data, error }) => {
      if (data) setTables(data.data);
      else setError(errorMessage(error));
    });
  }, [brand]);
  const choices = useMemo<BuilderChoices>(() => {
    const fields: BuilderChoices["fields"] = {};
    for (const t of cat.catalog?.types ?? []) {
      if (t.key === "person" || t.key === "account") fields[t.key] = t.fields.filter((f) => !f.archived).map((f) => ({ key: f.key, label: f.label, value_type: f.value_type }));
    }
    return { tables, fields };
  }, [tables, cat.catalog]);
  return { choices, tables, error };
}

function Editor({ ws, brand, currency, id, canManage, canPublish, onOpen, go }: { ws: string; brand: string; currency: string; id: string; canManage: boolean; canPublish: boolean; onOpen: (item: string) => void; go: (t: Tab, item?: string) => void }) {
  const { choices, tables, error: tablesError } = useChoices(ws, brand);
  // What is saved (null for a new query) and what is on the page now.
  const [saved, setSaved] = useState<Saved | null>(null);
  const [loading, setLoading] = useState(!!id);
  const [name, setName] = useState("");
  const [question, setQuestion] = useState("");
  // What is typed in the describe box: the whole query, or a change to it.
  const [prompt, setPrompt] = useState("");
  const [explanation, setExplanation] = useState("");
  const [builder, setBuilder] = useState<Builder | null>(null);
  const [sql, setSQL] = useState("");
  const [chart, setChart] = useState<ChartSpec | null>(null);
  const [mode, setMode] = useState<"builder" | "sql">("builder");
  const [showSQL, setShowSQL] = useState(false);
  const [buildError, setBuildError] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const [ranSQL, setRanSQL] = useState("");
  const [runError, setRunError] = useState("");
  const [running, setRunning] = useState(false);
  const [view, setView] = useState<"table" | "chart">("table");
  const [viewing, setViewing] = useState<Version | null>(null);
  const [showVersions, setShowVersions] = useState(false);
  const [bulk, setBulk] = useState(false);
  const [saving, setSaving] = useState(false);
  const asking = useAsk(brand);
  const editor = useRef<EditorView | null>(null);

  function load(q: Saved) {
    setSaved(q);
    setName(q.name);
    setQuestion(q.question);
    setBuilder((q.builder as Builder | null) ?? null);
    setSQL(q.sql);
    setChart(q.chart ?? null);
    setView(q.chart ? "chart" : "table");
    setMode(q.builder ? "builder" : "sql");
    setShowSQL(false);
    setViewing(null);
    nameItem(q.id, q.name);
  }

  useEffect(() => {
    if (!id) {
      const d = pendingDraft;
      pendingDraft = null;
      if (d) {
        setName(d.name);
        setQuestion(d.question);
        setExplanation(d.explanation);
        setBuilder(d.builder);
        setSQL(d.sql);
        setMode(d.builder || !d.question ? (d.builder ? "builder" : "sql") : "sql");
      } else {
        setBuilder(emptyBuilder("people"));
      }
      return;
    }
    api.GET("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand, saved_query: id } } }).then(({ data, error }) => {
      setLoading(false);
      if (!data) return setRunError(errorMessage(error));
      load(data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand, id]);

  // The builder's SQL, written by the engine as the builder changes.
  useEffect(() => {
    if (!builder || mode !== "builder") return;
    const t = setTimeout(async () => {
      const { data, error } = await api.POST("/v1/brands/{brand}/query/build", { params: { path: { brand } }, body: { builder } });
      if (!data) return setBuildError(errorMessage(error));
      setBuildError("");
      setSQL(data.sql);
    }, 350);
    return () => clearTimeout(t);
  }, [builder, mode, brand]);

  const run = useCallback(async (text: string) => {
    text = text.trim();
    if (!text) return;
    setRunning(true);
    setRunError("");
    const res = await api.POST("/v1/brands/{brand}/query", { params: { path: { brand } }, body: { sql: text, limit: rowsShown } });
    setRunning(false);
    if (!res.data) {
      setResult(null);
      return setRunError(errorMessage(res.error));
    }
    setResult(res.data);
    setRanSQL(text);
  }, [brand]);

  // A builder's results follow it: each change runs once its SQL is written.
  useEffect(() => {
    if (mode === "builder" && sql && !buildError && sql !== ranSQL) run(sql);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sql, mode, buildError]);

  const baseline = saved
    ? { name: saved.name, question: saved.question, builder: saved.builder ?? null, sql: saved.sql.trim(), chart: saved.chart ?? null }
    : { name: "", question: "", builder: null, sql: "", chart: null };
  const current = { name: name.trim(), question: question.trim(), builder: mode === "builder" ? builder : null, sql: sql.trim(), chart };
  const dirty = !same(baseline, current);
  const canEdit = canManage && (!saved || saved.can_edit) && !viewing;
  const asList = !!result?.columns.some((c) => c.name === "record_id");

  async function describe() {
    const text = prompt.trim();
    const change = !!(saved || question.trim());
    const q = await asking.run(text, !change ? undefined : mode === "builder" && builder ? { builder } : sql.trim() ? { sql } : undefined);
    if (!q) return;
    // The saved description says the whole query: the first ask, then each change.
    setQuestion(change && question.trim() ? `${question.trim()}\nThen: ${text}`.slice(-2000) : text);
    setPrompt("");
    if (!name.trim()) setName(q.name);
    setExplanation(q.explanation);
    if (q.builder) {
      setBuilder(q.builder as Builder);
      setMode("builder");
    } else {
      setBuilder(null);
      setMode("sql");
      setSQL(q.sql);
      run(q.sql);
    }
  }

  function cancel() {
    if (!saved) return onOpen("");
    load(saved);
    setExplanation("");
    toast("Changes undone.");
  }

  async function save(asNew = false) {
    if (!sql.trim()) return toast("Build or write the query first.", "error");
    let finalName = name.trim();
    if (!finalName || asNew) {
      const r = await ask({
        title: asNew ? "Save as a new query" : "Name this query",
        body: "Saved queries are private to you until you share them with the brand.",
        input: { label: "Name", maxLength: 200, value: asNew ? `${finalName} (copy)` : finalName },
        choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "save", variant: "primary" }],
      });
      if (!r?.value.trim()) return;
      finalName = r.value.trim();
    }
    const body = {
      name: finalName,
      question: question.trim(),
      chart,
      ...(mode === "builder" && builder ? { builder } : { sql: sql.trim(), builder: null }),
    };
    setSaving(true);
    const res = saved && !asNew
      ? await api.PATCH("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand, saved_query: saved.id } }, body })
      : await api.POST("/v1/brands/{brand}/queries", { params: { path: { brand } }, body: { ...body, builder: mode === "builder" ? builder : undefined } });
    setSaving(false);
    if (!res.data || !("id" in res.data)) return toast(errorMessage(res.error), "error");
    const q = res.data;
    toast(saved && !asNew ? (q.current_version > saved.current_version ? `Saved as version ${q.current_version}.` : "Saved.") : "Query saved.");
    load(q);
    if (!saved || asNew) onOpen(q.id);
  }

  async function patch(body: { visibility?: "private" | "brand"; description?: string }, done: string) {
    if (!saved) return;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand, saved_query: saved.id } }, body });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast(done);
    setSaved(data);
  }

  async function archive() {
    if (!saved) return;
    const ok = await confirmAction("Archive this query?", <p>It leaves the list for everyone it is shared with, and can no longer be run or changed. Its versions are kept, and lists made from it keep their last members.</p>, "Archive");
    if (!ok) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/queries/{saved_query}/archive", { params: { path: { brand, saved_query: saved.id } } });
    if (!data) return toast(errorMessage(error), "error");
    toast("Query archived.");
    onOpen("");
  }

  async function makeList() {
    if (!saved) return;
    if (dirty) return toast("Save your changes first: the list follows the saved query.", "error");
    const r = await ask({
      title: "Make it a list",
      body: "A list of everyone this query returns, kept up to date every 15 minutes (up to 50,000). Use it in sequences, broadcasts, journeys and ad audiences.",
      input: { label: "List name", maxLength: 200, value: saved.name },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Make the list", value: "ok", variant: "primary" }],
    });
    if (!r?.value.trim()) return;
    const recordType = builder?.from === "companies" ? "account" : "person";
    const { data, error } = await api.POST("/v1/brands/{brand}/smart-lists", { params: { path: { brand } }, body: { kind: "query", saved_query_id: saved.id, name: r.value.trim(), record_type: recordType } });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast(`Made the list ${data.name}. Filling it now.`);
    go("lists", data.id);
  }

  async function download() {
    const text = sql.trim();
    if (!text) return;
    const res = saved && !dirty && !viewing
      ? await api.POST("/v1/brands/{brand}/queries/{saved_query}/csv", { params: { path: { brand, saved_query: saved.id } }, body: {}, parseAs: "blob" })
      : await api.POST("/v1/brands/{brand}/query/csv", { params: { path: { brand } }, body: { sql: text }, parseAs: "blob" });
    if (res.error || !(res.data instanceof Blob)) return toast(errorMessage(res.error), "error");
    const file = /filename="([^"]+)"/.exec(res.response.headers.get("Content-Disposition") ?? "")?.[1] ?? "outflare-query.csv";
    const url = URL.createObjectURL(res.data);
    const a = document.createElement("a");
    a.href = url;
    a.download = file;
    a.click();
    URL.revokeObjectURL(url);
    const n = Number(res.response.headers.get("Outflare-Query-Rows") ?? 0);
    toast(res.response.headers.get("Outflare-Query-Truncated") === "true" ? `Downloaded the first ${plural(n, "row")}: the query has more.` : `Downloaded ${plural(n, "row")}.`);
  }

  async function editByHand() {
    const ok = await confirmAction("Edit the SQL by hand?", <p>The builder can't read SQL changed by hand, so it turns off for this query. You can still describe changes in English, or start the builder again.</p>, "Edit the SQL", false);
    if (!ok) return;
    setMode("sql");
    setShowSQL(false);
    track("ui.query_sql_edited", { from_builder: true });
  }

  function backToBuilder() {
    setShowSQL(false);
    setBuilder(builder ?? emptyBuilder(choices.tables.some((t) => t.name === "people") ? "people" : choices.tables.find((t) => t.available)?.name ?? "people"));
    setMode("builder");
  }

  if (loading) return <Loading />;
  if (id && !saved) return <div className="space-y-4"><PageHeader title="Query" back={{ label: "Queries", onClick: () => onOpen("") }} /><ErrorText>{runError || "This query wasn't found."}</ErrorText></div>;

  const side = mode === "builder" && !!builder && !showSQL;
  const hasQuery = !!(saved || question.trim());
  const moneyLabels = mode === "builder" && builder ? builder.columns.filter((c) => c.divide_by === 100).map((c) => c.label) : [];
  const schema: SqlSchema = Object.fromEntries(tables.filter((t) => t.available).map((t) => [t.name, t.columns.map((c) => c.name)]));
  return (
    <div className="space-y-5">
      <PageHeader
        back={{ label: "Queries", onClick: () => onOpen("") }}
        title={
          <input
            aria-label="Query name"
            className="-ml-1 max-w-full min-w-0 rounded bg-transparent px-1 text-xl font-semibold tracking-tight outline-none hover:bg-accent/50 focus:bg-accent/50"
            placeholder="Untitled query"
            size={Math.min(60, Math.max(14, name.length + 2))}
            maxLength={200}
            disabled={!canEdit}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        }
        description={
          saved ? (
            <span className="flex flex-wrap items-center gap-2">
              <Badge tone={saved.visibility === "brand" ? "blue" : "grey"}>{saved.visibility === "brand" ? "Shared with the brand" : "Only you"}</Badge>
              <span>Version {saved.current_version} · changed <span title={stamp(saved.updated_at)}>{ago(saved.updated_at)}</span></span>
              {dirty && <Badge tone="amber">Not saved</Badge>}
            </span>
          ) : (
            <span>New query{dirty ? " · not saved" : ""}</span>
          )
        }
        action={
          <>
            {saved && (
              <Menu
                items={[
                  asList && !!saved.can_edit && { label: "Make it a list", onClick: makeList },
                  { label: "Download CSV", onClick: download },
                  { label: showVersions ? "Hide versions" : "Versions", onClick: () => { setShowVersions(!showVersions); if (!showVersions) track("ui.query_versions_opened", { versions: saved.current_version }); } },
                  canManage && { label: "Save as a new query", onClick: () => save(true) },
                  saved.mine && canManage && { label: saved.visibility === "brand" ? "Make private" : "Share with the brand", onClick: () => patch({ visibility: saved.visibility === "brand" ? "private" : "brand" }, saved.visibility === "brand" ? "Only you see it now." : "Shared with everyone who can query this brand.") },
                  canPublish && { label: "Publish as an API endpoint", onClick: () => (dirty ? toast("Save your changes first.", "error") : onOpen(`${saved.id}/publish`)) },
                  canManage && !dirty && asList && { label: "Bulk edit its records", onClick: () => setBulk(true) },
                  saved.can_edit && { label: "Archive", onClick: archive, danger: true },
                ]}
              />
            )}
            {canEdit && (
              <>
                <Button variant="ghost" onClick={cancel} disabled={saved ? !dirty : false}>Cancel</Button>
                <Button onClick={() => save()} disabled={saving || (saved ? !dirty : !sql.trim())}>{saving ? "Saving…" : "Save"}</Button>
              </>
            )}
          </>
        }
      />
      {bulk && saved && <BulkEditSheet ws={ws} brand={brand} source={{ kind: "query", id: saved.id, name: saved.name }} onClose={() => setBulk(false)} onDone={() => setBulk(false)} />}
      {viewing && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-amber-600/30 bg-amber-500/10 px-3 py-2 text-[13px]">
          <span className="flex-1">Viewing version {viewing.version}{viewing.note ? `: ${viewing.note}` : ""}. Restore it below to make it current, or go back.</span>
          <Button variant="ghost" onClick={() => saved && load(saved)}>Back to current</Button>
        </div>
      )}

      <section aria-label="Describe it" className="space-y-2 rounded-lg border bg-card p-3 shadow-xs">
        <DescribeBox
          value={prompt}
          onChange={setPrompt}
          onSubmit={describe}
          busy={asking.busy}
          submitLabel={hasQuery ? "Change it" : "Build it"}
          placeholder={hasQuery ? 'Describe a change in plain English, such as "also show their country" or "only people who signed up this year"' : `Describe what you want to see, such as: ${examples[0]}`}
          error={asking.error}
          note={asking.note}
          aiLink={() => go("ai")}
        />
        {(question || explanation) && (
          <div className="space-y-1 border-t pt-2 text-[12px] text-muted-foreground">
            {explanation && <p className="flex gap-1.5 text-foreground"><Icon name="check" className="mt-0.5 size-3.5 shrink-0 text-emerald-500" />{explanation}</p>}
            {question && <p className="line-clamp-2" title={question}>Built from: “{question}”</p>}
          </div>
        )}
      </section>

      <div className={cn("grid grid-cols-[minmax(0,1fr)] gap-5", side && "xl:grid-cols-[minmax(340px,400px)_minmax(0,1fr)] xl:items-start")}>
        <section aria-label="Query" className={cn("min-w-0 space-y-3 rounded-lg border bg-card p-3 shadow-xs", side && "xl:sticky xl:top-3 xl:max-h-[calc(100vh-1.5rem)] xl:overflow-y-auto")}>
          <div className="flex flex-wrap items-center gap-3">
            <Tabs
              value={mode === "builder" && !showSQL ? "builder" : "sql"}
              onValueChange={(v) => {
                if (v === "sql") setShowSQL(true);
                else if (mode === "sql") backToBuilder();
                else setShowSQL(false);
              }}
            >
              <TabsList className="h-8">
                <TabsTrigger value="builder" className="px-3 text-[12px]">Builder</TabsTrigger>
                <TabsTrigger value="sql" className="px-3 text-[12px]">SQL</TabsTrigger>
              </TabsList>
            </Tabs>
            {!side && mode === "builder" && builder && <span className="text-[12px] text-muted-foreground">{builderSummary(builder)}</span>}
          </div>
          {tablesError && <ErrorText>{tablesError}</ErrorText>}
          {sqlOrBuilder()}
        </section>

        <section aria-label="Results" className="min-w-0 space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-[15px] font-medium">Results</h2>
            {running && <span className="text-[12px] text-muted-foreground">Running…</span>}
            <div className="flex-1" />
            <Button variant="ghost" onClick={download} disabled={!sql.trim()}>Download CSV</Button>
            {saved && asList && !!saved.can_edit && <Button variant="ghost" onClick={makeList}>Make it a list</Button>}
          </div>
          {runError && <ErrorText>{runError}</ErrorText>}
          {buildError && mode === "builder" && <ErrorText>{buildError}</ErrorText>}
          {result ? (
            <Results ws={ws} result={result} stale={ranSQL !== sql.trim()} view={view} setView={setView} chart={chart} setChart={setChart} money={moneyLabels} currency={currency} />
          ) : (
            !runError && <p className="rounded-lg border border-dashed p-6 text-center text-[13px] text-muted-foreground">{mode === "builder" ? "Results show here as you build." : "Run the SQL to see its results."}</p>
          )}
        </section>
      </div>

      {saved && showVersions && (
        <Versions brand={brand} query={saved} canRestore={canManage && saved.can_edit} onView={(v) => { setViewing(v); setSQL(v.sql); setChart(v.chart ?? null); setBuilder((v.builder as Builder | null) ?? null); setMode(v.builder ? "builder" : "sql"); }} onRestored={(q) => load(q)} />
      )}
    </div>
  );

  function sqlOrBuilder() {
    if (mode === "builder" && builder && !showSQL) {
      return <BuilderEditor value={builder} onChange={setBuilder} choices={choices} readOnly={!!viewing} />;
    }
    if (mode === "builder" && builder) {
      return (
        <div className="space-y-2">
          <p className="text-[12px] text-muted-foreground">The SQL the builder writes. It changes as you change the builder.</p>
          <div className="relative">
            <pre className="max-h-96 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-[12px]">{sql}</pre>
            <div className="absolute top-2 right-2"><CopyButton text={sql} /></div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="ghost" onClick={() => setShowSQL(false)}>Back to the builder</Button>
            {canEdit && <Button variant="ghost" onClick={editByHand}>Edit the SQL by hand</Button>}
          </div>
        </div>
      );
    }
    return (
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_260px]">
        <div className="min-w-0 space-y-2">
          <SqlEditor value={sql} onChange={setSQL} onRun={() => run(sql)} schema={schema} editor={editor} readOnly={!!viewing} label="SQL" />
          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={() => run(sql)} disabled={running || !sql.trim()}>{running ? "Running…" : "Run"}</Button>
            <span className="text-[12px] text-muted-foreground">⌘/Ctrl + Enter</span>
            <CopyButton text={sql} />
            <div className="flex-1" />
            {!viewing && <Button variant="ghost" onClick={backToBuilder}>{builder ? "Back to the builder" : "Use the builder instead"}</Button>}
          </div>
          {!builder && <p className="text-[12px] text-muted-foreground">This query is SQL. Describe a change above in plain English, or use the builder to start again without SQL.</p>}
        </div>
        <aside className="space-y-3">
          <Tables tables={tables} onInsert={(t, kind) => { insertAt(editor.current, t); track("ui.query_table_inserted", { kind }); }} />
          <CatalogTypes ws={ws} brand={brand} tables={tables} onInsert={(t, kind) => { insertAt(editor.current, t); track("ui.query_table_inserted", { kind }); }} />
        </aside>
      </div>
    );
  }
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

const isoTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/;
const isoDate = /^\d{4}-\d{2}-\d{2}$/;

/** cellText writes a value the way people read it: dates as dates, money as money, nothing as a dash. */
function cellText(v: unknown, isMoney: boolean, currency: string): { text: string; title?: string; muted?: boolean; mono?: boolean } {
  if (v === null || v === undefined || v === "") return { text: "—", muted: true };
  if (typeof v === "boolean") return { text: v ? "Yes" : "No" };
  if (typeof v === "number") {
    if (isMoney) {
      try {
        return { text: v.toLocaleString(undefined, { style: "currency", currency: currency || "USD" }), title: String(v) };
      } catch {
        return { text: v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) };
      }
    }
    return { text: v.toLocaleString(), title: String(v) };
  }
  if (typeof v === "string") {
    if (isoTime.test(v)) {
      const d = new Date(v);
      if (!Number.isNaN(d.getTime())) {
        const midnight = d.getHours() === 0 && d.getMinutes() === 0 && d.getSeconds() === 0;
        return { text: midnight ? d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : d.toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "numeric", minute: "2-digit" }), title: stamp(v) };
      }
    }
    if (isoDate.test(v)) {
      const d = new Date(`${v}T00:00:00`);
      if (!Number.isNaN(d.getTime())) return { text: d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }), title: v };
    }
    return { text: v, title: v.length > 40 ? v : undefined };
  }
  return { text: JSON.stringify(v), mono: true, title: JSON.stringify(v) };
}

// Columns named like money (revenue, spend, amount, price…) that aren't in cents show as money too.
const moneyName = /(revenue|spend|amount|price|mrr|arr|ltv|value)$/i;

type Row = { i: number; r: unknown[] };

function Results({ ws, result, stale, view, setView, chart, setChart, money, currency }: {
  ws: string; result: Result; stale: boolean; view: "table" | "chart"; setView: (v: "table" | "chart") => void; chart: ChartSpec | null; setChart: (c: ChartSpec | null) => void; money: string[]; currency: string;
}) {
  const nums = numericColumns(result.columns, result.rows);
  const spec = chart ?? defaultChart(result.columns, result.rows);
  // record_id links each row to its person or company, so it isn't a column of its own.
  const link = result.columns.findIndex((c) => c.name === "record_id");
  const shown = result.columns.map((_, i) => i).filter((i) => i !== link || result.columns.length === 1);
  const isMoney = (name: string) => money.includes(name) || (moneyName.test(name.replace(/[\s_]+/g, "")) && !/(_minor|_cents)$/.test(name) && nums.includes(name));
  const rows = useMemo<Row[]>(() => result.rows.map((r, i) => ({ i, r })), [result]);
  const columns = useMemo<ColumnDef<Row, unknown>[]>(
    () =>
      shown.map((j, k) => {
        const col = result.columns[j];
        const m = isMoney(col.name);
        const numeric = nums.includes(col.name);
        return {
          id: `c${j}`,
          header: col.name,
          accessorFn: (row: Row) => {
            const v = row.r[j];
            return typeof v === "object" && v !== null ? JSON.stringify(v) : v;
          },
          sortUndefined: "last",
          meta: numeric ? { align: "right" as const } : undefined,
          cell: ({ row: { original } }: { row: { original: Row } }) => {
            const c = cellText(original.r[j], m, currency);
            const id = k === 0 && link >= 0 && typeof original.r[link] === "string" ? (original.r[link] as string) : "";
            const body = <span className={cn("block max-w-96 truncate", c.muted && "text-muted-foreground", c.mono && "font-mono text-[12px]", numeric && "tabular-nums")} title={c.title}>{c.text}</span>;
            return id ? (
              <a className="block truncate font-medium underline decoration-muted-foreground/40 underline-offset-4 hover:decoration-foreground" href={appPath(ws, "people", id)} title="Open this person or company">
                {c.muted ? "Open" : c.text}
              </a>
            ) : body;
          },
        };
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [result, money.join("|"), currency, ws],
  );
  function show(v: "table" | "chart") {
    setView(v);
    if (v === "chart") {
      if (!chart && spec) setChart(spec);
      track("ui.query_chart_shown", { type: spec?.type ?? "none", series: spec?.y.length ?? 0, rows: result.row_count });
    }
  }
  const toggle = (
    <div role="tablist" className="inline-flex rounded-md border p-0.5 text-[12px]">
      {(["table", "chart"] as const).map((v) => (
        <button key={v} type="button" role="tab" aria-selected={view === v} className={cn("rounded px-2 py-0.5", view === v ? "bg-accent text-foreground" : "text-muted-foreground")} onClick={() => show(v)}>{v === "table" ? "Table" : "Chart"}</button>
      ))}
    </div>
  );
  const status = (
    <span className="flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
      {plural(result.row_count, "row")} in {result.duration_ms} ms
      {result.truncated && <Badge tone="amber" title="Narrow the query, or download the CSV for up to 10,000 rows.">More rows than shown</Badge>}
      {stale && <Badge tone="amber">Changed since this ran</Badge>}
    </span>
  );
  if (view === "chart" || result.columns.length === 0) {
    return (
      <div className="space-y-3 rounded-lg border bg-card p-3 shadow-xs">
        <div className="flex flex-wrap items-center gap-2">{status}<div className="flex-1" />{toggle}</div>
        {result.columns.length === 0 ? (
          <Empty title="No columns" />
        ) : spec ? (
          <>
            <ChartControls columns={result.columns.map((c) => c.name)} nums={nums} spec={spec} onChange={setChart} />
            <QueryChart columns={result.columns} rows={result.rows} spec={spec} />
          </>
        ) : (
          <p className="p-4 text-[13px] text-muted-foreground">A chart needs a number column, such as a count or a total.</p>
        )}
      </div>
    );
  }
  return (
    <DataTable<Row>
      data={rows}
      rowId={(row) => String(row.i)}
      columns={columns}
      searchPlaceholder="Search these results"
      toolbar={<>{status}{toggle}</>}
      empty="No rows match. Loosen a filter to see more."
      columnsMenu
    />
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
