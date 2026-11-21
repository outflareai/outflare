import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { RefreshCw, Trash2 } from "lucide-react";
import { Badge, Button, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { appPath } from "@/lib/paths";
import { nameItem } from "@/lib/records";
import { cn } from "@/lib/utils";
import { PickBrand } from "@/components/pick-brand";
import { DataTable } from "@/components/data-table";
import { Picker } from "@/components/query-builder";
import type { Tab } from "../App";

// Published query endpoints (docs/query-endpoints.md), in the Data menu: a
// saved query that another system calls with a brand API token holding
// query.endpoints. The list comes first; each endpoint opens on a page of its
// own (/query-endpoints/<id>). Owners and admins publish (from a saved
// query's page, /query/<id>/publish), turn them off and on, and delete them;
// everyone who can query sees them and their calls.

type Endpoint = Schemas["QueryEndpoint"];
type Param = Schemas["QueryEndpointParam"];
type Call = Schemas["QueryEndpointCall"];

const types: Param["type"][] = ["text", "integer", "number", "boolean", "date", "timestamp", "uuid"];
const slugify = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63);

// deleteEndpoint asks first, then deletes the endpoint for good. It says what
// goes (the address and the call log) and what stays (the saved query, the
// audit log), and points to turning it off for a pause. True once deleted.
async function deleteEndpoint(brand: string, e: Endpoint): Promise<boolean> {
  const ok = await confirmAction(
    `Delete ${e.name}?`,
    <div className="space-y-2">
      <p>
        Calls to <span className="font-mono">/{e.slug}</span> stop working straight away and answer 404
        {e.calls_24h > 0 ? <>. It was called {plural(e.calls_24h, "time")} in the last 24 hours, so check nothing still uses it</> : null}.
      </p>
      <p>Its call log is deleted with it. The saved query stays, and the audit log keeps a record of the endpoint. The address can be used again.</p>
      <p className="text-muted-foreground">To stop calls for a while instead, turn it off.</p>
    </div>,
    "Delete endpoint",
  );
  if (!ok) return false;
  const { error } = await api.DELETE("/v1/brands/{brand}/query-endpoints/{endpoint}", { params: { path: { brand, endpoint: e.id } } });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast(`Deleted ${e.name}.`);
  return true;
}

export function useEndpoints(brand: string) {
  const [list, setList] = useState<Endpoint[] | null>(null);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/query-endpoints", { params: { path: { brand } } });
    setList(data?.data ?? []);
  }, [brand]);
  useEffect(() => {
    load();
  }, [load]);
  return { list, load };
}

// useDescribe writes an endpoint's description from its saved query, its
// parameters and its row cap (POST .../query-endpoints/describe: rules, no AI),
// again a moment after they change. Null until the first one arrives.
function useDescribe(brand: string, query: string, params: Param[], rowCap: number, version?: number, on = true) {
  const [text, setText] = useState<string | null>(null);
  const key = JSON.stringify([params, rowCap, version]);
  useEffect(() => {
    if (!on) return;
    let stale = false;
    const t = setTimeout(async () => {
      const valid = params.filter((p) => /^[a-z_][a-z0-9_]{0,40}$/.test(p.name));
      const { data } = await api.POST("/v1/brands/{brand}/query-endpoints/describe", {
        params: { path: { brand } },
        body: { saved_query_id: query, version, params: valid.length === params.length ? params : [], row_cap: rowCap >= 1 && rowCap <= 10000 ? rowCap : undefined },
      });
      if (!stale && data) setText(data.description);
    }, 300);
    return () => {
      stale = true;
      clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand, query, key, on]);
  return text;
}

export function QueryEndpoints({ ws, canPublish, open, onOpen, go }: { ws: string; canPublish: boolean; open: string; onOpen: (item: string) => void; go: (t: Tab, item?: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("query-endpoints", "Endpoints"), []);
  if (!brand) {
    return (
      <div className="space-y-6">
        <PageHeader title="Endpoints" />
        <PickBrand what="Endpoints" brands={active.length} />
      </div>
    );
  }
  if (open) return <EndpointPage key={open} ws={ws} brand={brand.id} id={open} canPublish={canPublish} onBack={() => onOpen("")} go={go} />;
  return <EndpointList key={brand.id} ws={ws} brand={brand.id} canPublish={canPublish} onOpen={onOpen} go={go} />;
}

// PublishPicker picks a saved query to publish, from the Endpoints page.
function PublishPicker({ brand, go, label = "Publish a saved query" }: { brand: string; go: (t: Tab, item?: string) => void; label?: string }) {
  const [saved, setSaved] = useState<Schemas["SavedQuery"][]>([]);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/queries", { params: { path: { brand } } }).then(({ data }) => setSaved(data?.data ?? []));
  }, [brand]);
  if (saved.length === 0) return <Button onClick={() => go("query")}>Make a query first</Button>;
  return (
    <Picker
      label={label}
      placeholder={label}
      className="h-9 w-64 border-primary/40 font-medium"
      value=""
      items={saved.map((q) => ({ value: q.id, label: q.name, group: q.visibility === "brand" ? "Shared with the brand" : "Only you", hint: `v${q.current_version}` }))}
      onPick={(id) => go("query", `${id}/publish`)}
    />
  );
}

function EndpointList({ ws, brand, canPublish, onOpen, go }: { ws: string; brand: string; canPublish: boolean; onOpen: (item: string) => void; go: (t: Tab, item?: string) => void }) {
  const { list, load } = useEndpoints(brand);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Endpoints"
        description="Saved queries that another system, such as a BI tool, a spreadsheet or your own app, calls over the API with a token that can do nothing else. Every call is logged."
        action={canPublish ? <PublishPicker brand={brand} go={go} /> : <Button variant="ghost" onClick={() => go("query")}>Open queries</Button>}
      />
      {list === null ? (
        <Loading />
      ) : list.length === 0 ? (
        <section className="rounded-lg border border-dashed p-6">
          <h2 className="text-[15px] font-medium">No endpoints yet</h2>
          <ol className="mt-3 max-w-xl list-decimal space-y-1.5 pl-5 text-[13px] text-muted-foreground">
            <li>Make and save a query in <a className="text-primary hover:underline" href={appPath(ws, "query")} onClick={(e) => { e.preventDefault(); go("query"); }}>Queries</a>. Use $1, $2… in its SQL for values the caller sends.</li>
            <li>Publish it here, or from the query's menu: give it an address, a row cap and a rate limit.</li>
            <li>Make an API token for this brand with only "Call query endpoints", and call the address with it.</li>
          </ol>
          {canPublish ? <div className="mt-4"><PublishPicker brand={brand} go={go} /></div> : <p className="mt-4 text-[12px] text-muted-foreground">Only owners and admins publish endpoints.</p>}
        </section>
      ) : (
        <DataTable<Endpoint>
          data={list}
          rowId={(e) => e.id}
          onRowClick={(e) => onOpen(e.id)}
          searchPlaceholder="Search endpoints"
          empty="No endpoints yet. Open a saved query and pick Publish as an API endpoint."
          initialSort={[{ id: "name", desc: false }]}
          mobile={(e) => (
            <div className="space-y-0.5">
              <div className="font-medium">{e.name}</div>
              <div className="text-[12px] text-muted-foreground">/{e.slug} · {e.status === "active" ? "On" : "Off"} · {plural(e.calls_24h, "call")} today</div>
            </div>
          )}
          columns={[
            {
              id: "name",
              header: "Name",
              accessorFn: (e) => `${e.name} ${e.slug} ${e.description}`,
              cell: ({ row: { original: e } }) => (
                <a href={appPath(ws, "query-endpoints", e.id)} onClick={(ev) => ev.preventDefault()} className="block">
                  <span className="font-medium">{e.name}</span>
                  <span className="block font-mono text-[12px] text-muted-foreground">/{e.slug}</span>
                  {e.description && <span className="block max-w-xl truncate text-[12px] text-muted-foreground" title={e.description}>{e.description}</span>}
                </a>
              ),
            },
            {
              id: "status",
              header: "Status",
              accessorFn: (e) => e.status,
              cell: ({ row: { original: e } }) => (
                <span className="flex items-center gap-1.5">
                  <span className={cn("size-2 rounded-full", e.status === "active" ? "bg-emerald-500" : "bg-muted-foreground/50")} aria-hidden />
                  {e.status === "active" ? "On" : "Off"}
                </span>
              ),
            },
            { id: "version", header: "Query version", accessorFn: (e) => e.version, meta: { align: "right" } },
            { id: "calls", header: "Calls today", accessorFn: (e) => e.calls_24h, meta: { align: "right" } },
            {
              id: "last",
              header: "Last call",
              accessorFn: (e) => e.last_call_at ?? "",
              cell: ({ row: { original: e } }) => (e.last_call_at ? <span title={stamp(e.last_call_at)}>{ago(e.last_call_at)}</span> : <span className="text-muted-foreground">Never</span>),
            },
            ...(canPublish
              ? [
                  {
                    id: "actions",
                    header: "",
                    enableSorting: false,
                    meta: { align: "right" as const },
                    cell: ({ row: { original: e } }: { row: { original: Endpoint } }) => (
                      <Button
                        variant="danger"
                        className="h-7 px-2"
                        aria-label={`Delete ${e.name}`}
                        title="Delete"
                        onClick={async (ev) => {
                          ev.stopPropagation();
                          if (await deleteEndpoint(brand, e)) load();
                        }}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    ),
                  },
                ]
              : []),
          ]}
        />
      )}
    </div>
  );
}

// PublishPage publishes a saved query (owners and admins): /query/<id>/publish.
export function PublishPage({ ws, brand, query: id, onBack, onDone }: { ws: string; brand: string; query: string; onBack: () => void; onDone: (e: Endpoint) => void }) {
  const [query, setQuery] = useState<Schemas["SavedQuery"] | null>(null);
  const [loadError, setLoadError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand, saved_query: id } } }).then(({ data, error }) => {
      if (data) {
        setQuery(data);
        nameItem(id, data.name);
      } else setLoadError(errorMessage(error));
    });
  }, [brand, id]);
  if (loadError) return <div className="space-y-4"><PageHeader title="Publish as an API endpoint" back={{ label: "Query", onClick: onBack }} /><ErrorText>{loadError}</ErrorText></div>;
  if (!query) return <Loading />;
  return <PublishForm ws={ws} brand={brand} query={query} onBack={onBack} onDone={onDone} />;
}

function PublishForm({ ws, brand, query, onBack, onDone }: { ws: string; brand: string; query: Schemas["SavedQuery"]; onBack: () => void; onDone: (e: Endpoint) => void }) {
  const used = Math.max(0, ...[...query.sql.matchAll(/\$(\d+)/g)].map((m) => Number(m[1])));
  const [name, setName] = useState(query.name);
  const [slug, setSlug] = useState(slugify(query.name));
  const [params, setParams] = useState<Param[]>(Array.from({ length: used }, (_, i) => ({ name: `param_${i + 1}`, type: "text", required: true })));
  const [rowCap, setRowCap] = useState(1000);
  const [rate, setRate] = useState(60);
  // The description is written from the query until someone types in it;
  // "Write it again" goes back to the written one.
  const [description, setDescription] = useState("");
  const [edited, setEdited] = useState(false);
  const auto = useDescribe(brand, query.id, params, rowCap);
  useEffect(() => {
    if (!edited && auto !== null) setDescription(auto);
  }, [auto, edited]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = (i: number, p: Partial<Param>) => setParams((ps) => ps.map((x, k) => (k === i ? { ...x, ...p } : x)));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/query-endpoints", {
      params: { path: { brand } },
      body: { saved_query_id: query.id, name, slug, description: description.trim() || undefined, params, row_cap: rowCap, rate_per_minute: rate },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "id" in data) {
      toast(`Published at /${data.slug}.`);
      onDone(data);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: query.name, onClick: onBack }}
        title="Publish as an API endpoint"
        description={<>Another system calls <a className="underline" href={appPath(ws, "query", query.id)}>{query.name}</a> (version {query.current_version}) with an API token made for this brand with only the "Call query endpoints" permission. It reads what you can read, and every call is logged.</>}
      />
      <form onSubmit={submit} className="max-w-2xl space-y-5 rounded-lg border bg-card p-4 text-[13px] shadow-xs">
        <label className="block">
          <span className="mb-1 block font-medium">Name</span>
          <input className={`${field} w-full`} required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <div>
          <div className="mb-1 flex items-center justify-between gap-2">
            <label htmlFor="endpoint-description" className="font-medium">Description</label>
            <Button type="button" variant="ghost" className="h-7 px-2 text-[12px]" disabled={auto === null || (!edited && description === auto)} onClick={() => { setEdited(false); if (auto !== null) setDescription(auto); }}>
              <RefreshCw className="size-3.5" />
              Write it again
            </Button>
          </div>
          <textarea id="endpoint-description" rows={3} maxLength={2000} className={`${field} w-full resize-y`} value={description} placeholder={auto === null ? "Writing a description from the query…" : ""} onChange={(e) => { setDescription(e.target.value); setEdited(true); }} />
          <span className="mt-1 block text-[12px] text-muted-foreground">
            {edited ? "Your own words. Write it again to go back to the one made from the query." : "Written from the query's filters, parameters, order and row limit, and kept up to date as you change them. Change it as you like."}
          </span>
        </div>
        <label className="block">
          <span className="mb-1 block font-medium">Address</span>
          <input className={`${field} w-full font-mono`} required pattern="[a-z0-9][a-z0-9-]{0,62}" value={slug} onChange={(e) => setSlug(e.target.value)} />
          <span className="mt-1 block text-[12px] text-muted-foreground">POST /v1/brands/…/query-endpoints/{slug || "…"}/run. It never changes once published.</span>
        </label>
        <div className="space-y-2">
          <span className="block font-medium">Parameters</span>
          <p className="text-[12px] text-muted-foreground">
            {used ? `The query uses ${used === 1 ? "$1" : `$1 to $${used}`}: name ${used === 1 ? "it" : "each, in order"}. Callers send parameters by name, and they are passed as values, never as SQL.` : "The query takes no parameters. Use $1, $2… in the SQL for values the caller sends."}
          </p>
          {params.map((p, i) => (
            <div key={i} className="flex flex-wrap items-center gap-2">
              <span className="w-8 font-mono text-muted-foreground">${i + 1}</span>
              <input aria-label={`Name of $${i + 1}`} className={`${field} flex-1`} value={p.name} onChange={(e) => set(i, { name: e.target.value })} />
              <select aria-label={`Type of $${i + 1}`} className={field} value={p.type} onChange={(e) => set(i, { type: e.target.value as Param["type"] })}>
                {types.map((t) => <option key={t}>{t}</option>)}
              </select>
              <label className="flex items-center gap-1">
                <input type="checkbox" checked={!!p.required} onChange={(e) => set(i, { required: e.target.checked })} /> required
              </label>
            </div>
          ))}
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1 block font-medium">Most rows a call returns</span>
            <input type="number" min={1} max={10000} className={`${field} w-full`} value={rowCap} onChange={(e) => setRowCap(Number(e.target.value))} />
          </label>
          <label className="block">
            <span className="mb-1 block font-medium">Calls a minute per token</span>
            <input type="number" min={1} max={600} className={`${field} w-full`} value={rate} onChange={(e) => setRate(Number(e.target.value))} />
          </label>
        </div>
        <ErrorText>{error}</ErrorText>
        <div className="flex flex-wrap gap-2 border-t pt-4">
          <Button disabled={busy || !name.trim() || !slug}>{busy ? "Publishing…" : "Publish"}</Button>
          <Button type="button" variant="ghost" onClick={onBack}>Cancel</Button>
        </div>
      </form>
    </div>
  );
}

// EndpointPage shows one endpoint: how to call it, its calls, and (for
// owners and admins) turning it off and on, and deleting it.
function EndpointPage({ ws, brand, id, canPublish, onBack, go }: { ws: string; brand: string; id: string; canPublish: boolean; onBack: () => void; go: (t: Tab, item?: string) => void }) {
  const [e, setE] = useState<Endpoint | null>(null);
  const [calls, setCalls] = useState<Call[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/query-endpoints/{endpoint}", { params: { path: { brand, endpoint: id } } }).then(({ data, error }) => {
      if (data && "id" in data) {
        setE(data);
        nameItem(id, data.name);
      } else setError(errorMessage(error));
    });
    api.GET("/v1/brands/{brand}/query-endpoints/{endpoint}/calls", { params: { path: { brand, endpoint: id }, query: { limit: 50 } } }).then(({ data }) => setCalls(data?.data ?? []));
  }, [brand, id]);

  async function toggle() {
    if (!e) return;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/query-endpoints/{endpoint}", {
      params: { path: { brand, endpoint: e.id } },
      body: { status: e.status === "active" ? "disabled" : "active" },
    });
    if (error) return toast(errorMessage(error), "error");
    if (data && "id" in data) {
      setE(data);
      toast(data.status === "active" ? "Turned on." : "Turned off: calls now answer 404.");
    }
  }

  if (error) return <div className="space-y-4"><PageHeader title="Endpoint" back={{ label: "Endpoints", onClick: onBack }} /><ErrorText>{error}</ErrorText></div>;
  if (!e) return <Loading />;
  const body = e.params.length ? ` \\\n  -d '{"params": {${e.params.map((p) => `"${p.name}": …`).join(", ")}}}'` : "";
  const example = `curl -X POST ${location.origin}/v1/brands/${brand}/query-endpoints/${e.slug}/run \\\n  -H "Authorization: Bearer <token>" -H "Content-Type: application/json"${body}`;
  return (
    <div className="space-y-6">
      <PageHeader
        back={{ label: "Endpoints", onClick: onBack }}
        title={e.name}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <Badge tone={e.status === "active" ? "green" : "amber"}>{e.status === "active" ? "On" : "Off"}</Badge>
            <span>Version {e.version} of <a className="underline" href={appPath(ws, "query", e.saved_query_id)} onClick={(ev) => { ev.preventDefault(); go("query", e.saved_query_id); }}>its saved query</a> · up to {e.row_cap.toLocaleString()} rows · {e.rate_per_minute} calls a minute per token · reads {e.scopes.join(", ")}</span>
          </span>
        }
        action={
          canPublish ? (
            <div className="flex flex-wrap gap-2">
              <Button variant={e.status === "active" ? "ghost" : "primary"} onClick={toggle}>{e.status === "active" ? "Turn off" : "Turn on"}</Button>
              <Button variant="danger" onClick={async () => { if (await deleteEndpoint(brand, e)) onBack(); }}>
                <Trash2 className="size-3.5" />
                Delete
              </Button>
            </div>
          ) : undefined
        }
      />
      <DescriptionCard brand={brand} e={e} canEdit={canPublish} onSaved={setE} />
      <section aria-label="Call it" className="space-y-2 rounded-lg border bg-card p-4 text-[13px] shadow-xs">
        <h2 className="font-medium">Call it</h2>
        <div className="relative">
          <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 text-[12px]">{example}</pre>
          <div className="absolute top-2 right-2"><CopyButton text={example} /></div>
        </div>
        <p className="text-[12px] text-muted-foreground">
          Make the token in <a className="underline" href={appPath(ws, "tokens")} onClick={(ev) => { ev.preventDefault(); go("tokens"); }}>API tokens</a>, for this brand, with only "Call query endpoints".
        </p>
        {e.params.length > 0 && (
          <div>
            <h3 className="mb-1 font-medium">Parameters</h3>
            <ul className="text-[12px]">
              {e.params.map((p, i) => <li key={p.name}><span className="font-mono">${i + 1} {p.name}</span> · {p.type}{p.required ? " · required" : ""}</li>)}
            </ul>
          </div>
        )}
      </section>
      <section aria-label="Latest calls" className="space-y-2">
        <h2 className="text-[15px] font-medium">Latest calls</h2>
        {calls === null ? (
          <Loading />
        ) : calls.length === 0 ? (
          <Empty title="No calls yet" />
        ) : (
          <DataTable<Call>
            data={calls}
            rowId={(c) => String(c.id)}
            initialSort={[{ id: "at", desc: true }]}
            columns={[
              { id: "at", header: "When", accessorFn: (c) => c.at, cell: ({ row: { original: c } }) => <span title={stamp(c.at)}>{ago(c.at)}</span> },
              {
                id: "outcome",
                header: "Outcome",
                accessorFn: (c) => c.outcome,
                cell: ({ row: { original: c } }) => (
                  <span className="flex items-center gap-1.5">
                    <span className={cn("size-2 rounded-full", c.outcome === "success" ? "bg-emerald-500" : "bg-destructive")} aria-hidden />
                    {c.outcome === "success" ? "Answered" : c.error_code}
                  </span>
                ),
              },
              { id: "rows", header: "Rows", accessorFn: (c) => c.rows, meta: { align: "right" }, cell: ({ row: { original: c } }) => <>{c.rows.toLocaleString()}{c.truncated ? " (capped)" : ""}</> },
              { id: "ms", header: "Took", accessorFn: (c) => c.duration_ms, meta: { align: "right" }, cell: ({ row: { original: c } }) => <>{c.duration_ms} ms</> },
              { id: "req", header: "Request id", accessorFn: (c) => c.request_id ?? "", cell: ({ row: { original: c } }) => (c.request_id ? <span className="flex items-center gap-1 font-mono text-[11px]">{c.request_id}<CopyButton text={c.request_id} /></span> : null) },
            ]}
          />
        )}
      </section>
    </div>
  );
}

// DescriptionCard shows what the endpoint returns, in plain English. Owners and
// admins edit it with Save and Cancel, and Write it again fills in the one
// made from the query (POST .../query-endpoints/describe).
function DescriptionCard({ brand, e, canEdit, onSaved }: { brand: string; e: Endpoint; canEdit: boolean; onSaved: (e: Endpoint) => void }) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(e.description);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const auto = useDescribe(brand, e.saved_query_id, e.params, e.row_cap, e.version, editing);

  async function save() {
    setBusy(true);
    setError("");
    const { data, error } = await api.PATCH("/v1/brands/{brand}/query-endpoints/{endpoint}", {
      params: { path: { brand, endpoint: e.id } },
      body: { description: text.trim() },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "id" in data) {
      onSaved(data);
      setEditing(false);
      toast("Description saved.");
    }
  }

  return (
    <section aria-label="Description" className="space-y-2 rounded-lg border bg-card p-4 text-[13px] shadow-xs">
      <div className="flex items-center justify-between gap-2">
        <h2 className="font-medium">What it returns</h2>
        {canEdit && !editing && (
          <Button variant="ghost" className="h-7 px-2 text-[12px]" onClick={() => { setText(e.description); setEditing(true); }}>
            Edit
          </Button>
        )}
        {editing && (
          <Button type="button" variant="ghost" className="h-7 px-2 text-[12px]" disabled={auto === null || text === auto} onClick={() => auto !== null && setText(auto)}>
            <RefreshCw className="size-3.5" />
            Write it again
          </Button>
        )}
      </div>
      {editing ? (
        <>
          <textarea aria-label="Description" rows={3} maxLength={2000} className={`${field} w-full resize-y`} value={text} onChange={(ev) => setText(ev.target.value)} />
          <ErrorText>{error}</ErrorText>
          <div className="flex gap-2">
            <Button disabled={busy || text === e.description} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
            <Button variant="ghost" onClick={() => { setEditing(false); setError(""); }}>Cancel</Button>
          </div>
        </>
      ) : e.description ? (
        <p className="whitespace-pre-wrap">{e.description}</p>
      ) : (
        <p className="text-muted-foreground">No description yet.{canEdit ? " Edit it and press Write it again to make one from the query." : ""}</p>
      )}
    </section>
  );
}
