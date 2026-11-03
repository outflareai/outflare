import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@outflare/sdk";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorText, field, Loading, toast } from "../ui";
import { ago, plural } from "../format";

// Published query endpoints (docs/query-endpoints.md): a saved query that
// another system calls with a brand API token holding query.endpoints.
// Owners and admins publish and change them; everyone who can query sees
// them and their calls.

type Endpoint = Schemas["QueryEndpoint"];
type Param = Schemas["QueryEndpointParam"];
type Call = Schemas["QueryEndpointCall"];

const types: Param["type"][] = ["text", "integer", "number", "boolean", "date", "timestamp", "uuid"];
const slugify = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63);

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

// EndpointsList sits beside the saved queries: the brand's endpoints, with
// their calls today.
export function EndpointsList({ list, onPick }: { list: Endpoint[] | null; onPick: (e: Endpoint) => void }) {
  if (!list || list.length === 0) return null;
  return (
    <section className="space-y-1">
      <h3 className="text-[12px] font-medium uppercase tracking-wide text-muted-foreground">Published endpoints</h3>
      <ul>
        {list.map((e) => (
          <li key={e.id}>
            <button type="button" onClick={() => onPick(e)} className="w-full truncate rounded px-2 py-1 text-left text-[13px] hover:bg-muted">
              {e.name} {e.status === "disabled" && <span className="text-[11px] text-muted-foreground">off</span>}
              <span className="block text-[11px] text-muted-foreground">/{e.slug} · {e.calls_24h} calls today</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

// PublishSheet publishes the open saved query (owners and admins).
export function PublishSheet({ brand, query, onClose, onDone }: { brand: string; query: { id: string; name: string; sql: string }; onClose: () => void; onDone: (e: Endpoint) => void }) {
  const used = Math.max(0, ...[...query.sql.matchAll(/\$(\d+)/g)].map((m) => Number(m[1])));
  const [name, setName] = useState(query.name);
  const [slug, setSlug] = useState(slugify(query.name));
  const [params, setParams] = useState<Param[]>(Array.from({ length: used }, (_, i) => ({ name: `param_${i + 1}`, type: "text", required: true })));
  const [rowCap, setRowCap] = useState(1000);
  const [rate, setRate] = useState(60);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = (i: number, p: Partial<Param>) => setParams((ps) => ps.map((x, k) => (k === i ? { ...x, ...p } : x)));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/query-endpoints", {
      params: { path: { brand } },
      body: { saved_query_id: query.id, name, slug, params, row_cap: rowCap, rate_per_minute: rate },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "id" in data) {
      toast(`Published at /${data.slug}.`);
      onDone(data);
    }
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Publish as an endpoint</SheetTitle>
          <SheetDescription>
            Another system calls it with an API token made for this brand with only the "Call query endpoints" permission. It runs this version of the query, reads what you can read, and logs every call.
          </SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-5 px-4 pb-6 text-[13px]">
          <label className="block">
            <span className="mb-1 block font-medium">Name</span>
            <input className={`${field} w-full`} required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
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
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}

// EndpointSheet shows one endpoint: how to call it, its calls, and (for
// owners and admins) turning it off and on.
export function EndpointSheet({ brand, endpoint, canPublish, onClose, onChanged }: { brand: string; endpoint: Endpoint; canPublish: boolean; onClose: () => void; onChanged: () => void }) {
  const [e, setE] = useState(endpoint);
  const [calls, setCalls] = useState<Call[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/query-endpoints/{endpoint}/calls", { params: { path: { brand, endpoint: e.id }, query: { limit: 20 } } }).then(({ data, error }) => {
      if (data) setCalls(data.data);
      else setError(errorMessage(error));
    });
  }, [brand, e.id]);

  async function toggle() {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/query-endpoints/{endpoint}", {
      params: { path: { brand, endpoint: e.id } },
      body: { status: e.status === "active" ? "disabled" : "active" },
    });
    if (error) return toast(errorMessage(error));
    if (data && "id" in data) {
      setE(data);
      toast(data.status === "active" ? "Turned on." : "Turned off: calls now answer 404.");
      onChanged();
    }
  }

  const body = e.params.length ? ` \\\n  -d '{"params": {${e.params.map((p) => `"${p.name}": …`).join(", ")}}}'` : "";
  const example = `curl -X POST ${location.origin}/v1/brands/${brand}/query-endpoints/${e.slug}/run \\\n  -H "Authorization: Bearer <token>" -H "Content-Type: application/json"${body}`;
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{e.name} {e.status === "disabled" && <Badge tone="amber">Off</Badge>}</SheetTitle>
          <SheetDescription>
            Version {e.version} of its saved query · up to {e.row_cap.toLocaleString()} rows · {e.rate_per_minute} calls a minute per token · reads {e.scopes.join(", ")}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4 pb-6 text-[13px]">
          <div>
            <p className="mb-1 font-medium">Call it</p>
            <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 text-[12px]">{example}</pre>
            <p className="mt-1 text-[12px] text-muted-foreground">Make the token under Settings → API tokens, for this brand, with only "Call query endpoints".</p>
          </div>
          {e.params.length > 0 && (
            <div>
              <p className="mb-1 font-medium">Parameters</p>
              <ul className="text-[12px]">
                {e.params.map((p, i) => <li key={p.name}><span className="font-mono">${i + 1} {p.name}</span> · {p.type}{p.required ? " · required" : ""}</li>)}
              </ul>
            </div>
          )}
          <div>
            <p className="mb-1 font-medium">Latest calls</p>
            <ErrorText>{error}</ErrorText>
            {calls === null && !error && <Loading />}
            {calls?.length === 0 && <p className="text-muted-foreground">No calls yet.</p>}
            {calls && calls.length > 0 && (
              <ul className="divide-y rounded-md border text-[12px]">
                {calls.map((c) => (
                  <li key={c.id} className="flex flex-wrap gap-x-3 px-3 py-1.5">
                    <span className={c.outcome === "success" ? "" : "text-destructive"}>{c.outcome === "success" ? plural(c.rows, "row") : c.error_code}</span>
                    <span className="text-muted-foreground">{c.duration_ms} ms</span>
                    <span className="text-muted-foreground">{ago(c.at)}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
          {canPublish && (
            <div className="flex flex-wrap gap-2 border-t pt-4">
              <Button variant={e.status === "active" ? "danger" : "primary"} onClick={toggle}>{e.status === "active" ? "Turn off" : "Turn on"}</Button>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
