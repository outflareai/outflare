import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, confirmAction, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { MovesSheet, StagesSheet } from "./LifecycleEditor";

// Products and lifecycle stages (docs/product-lifecycle.md): the products a
// brand sells, the names its events and billing use for them (held ones
// wait to be mapped), and the stages companies and people move through per
// product. RecordLifecycle shows where one record stands.

type Product = Schemas["Product"];
type ProductName = Schemas["ProductName"];
type Lifecycle = Schemas["ProductLifecycle"];
type RecordLifecycleView = Schemas["RecordProductLifecycle"];

const categoryTone: Record<string, "green" | "amber" | "red" | "grey" | "blue"> = {
  lead: "grey",
  trial: "blue",
  active: "green",
  at_risk: "amber",
  churned: "red",
};

export function Products({ ws, canManage, tabs }: { ws: string; canManage: boolean; tabs: ReactNode }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("products", "Products"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Products and lifecycle stages"
        description="The products this brand sells, and where each company and person stands with each one: trial, active, at risk or churned. A customer who cancels one product but keeps another is never treated as churned."
      />
      {tabs}
      {brand ? (
        <ProductsFor key={brand.id} brand={brand.id} canManage={canManage} />
      ) : (
        <Empty title="Pick a brand">
          Products belong to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher.
        </Empty>
      )}
    </div>
  );
}

function ProductsFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [products, setProducts] = useState<Product[] | null>(null);
  const [archived, setArchived] = useState(false);
  const [names, setNames] = useState<ProductName[]>([]);
  const [life, setLife] = useState<Lifecycle | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [aliases, setAliases] = useState("");
  const [churnMode, setChurnMode] = useState<"cancellation" | "inactivity">("cancellation");
  const [editing, setEditing] = useState<"stages" | "moves" | null>(null);

  const load = useCallback(async () => {
    const [p, n, l] = await Promise.all([
      api.GET("/v1/brands/{brand}/products", { params: { path: { brand }, query: { archived } } }),
      api.GET("/v1/brands/{brand}/product-names", { params: { path: { brand }, query: { status: "held" } } }),
      api.GET("/v1/brands/{brand}/lifecycle", { params: { path: { brand } } }),
    ]);
    if (!p.data || !n.data || !l.data) return setError(errorMessage(p.error ?? n.error ?? l.error));
    setError("");
    setProducts(p.data.data);
    setNames(n.data.data);
    setLife(l.data);
  }, [brand, archived]);
  useEffect(() => {
    void load();
  }, [load]);

  async function add() {
    setBusy(true);
    const body = { name: name.trim(), churn_mode: churnMode, aliases: aliases.split(",").map((a) => a.trim()).filter(Boolean) };
    const { error } = await api.POST("/v1/brands/{brand}/products", { params: { path: { brand } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`${body.name} added.`);
    setAdding(false);
    setName("");
    setAliases("");
    void load();
  }

  async function archive(p: Product) {
    const restore = !!p.archived_at;
    if (!restore && !(await confirmAction(`Archive ${p.name}?`, <p>It keeps its history, but its events no longer move anyone and it leaves companies' overall status. You can restore it.</p>, "Archive", true))) return;
    setBusy(true);
    const path = { params: { path: { brand, product: p.id } } };
    const { error } = restore ? await api.POST("/v1/brands/{brand}/products/{product}/restore", path) : await api.POST("/v1/brands/{brand}/products/{product}/archive", path);
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    void load();
  }

  async function mapName(n: ProductName, product: string) {
    setBusy(true);
    const body = product === "ignore" ? { status: "ignored" as const, product_id: null } : { status: "mapped" as const, product_id: product };
    const { error } = await api.PATCH("/v1/brands/{brand}/product-names/{name}", { params: { path: { brand, name: n.id } }, body });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(product === "ignore" ? `${n.value} ignored.` : `${n.value} mapped. Events from now on count for that product.`);
    void load();
  }

  async function toggle(on: boolean) {
    if (
      on &&
      !(await confirmAction(
        "Turn lifecycle stages on?",
        <p>Stages start empty and fill only from events from now on; nothing is rebuilt from history. Each move is an event that rules and journeys can start on.</p>,
        "Turn on",
        false,
      ))
    )
      return;
    setBusy(true);
    const { error } = await api.PATCH("/v1/brands/{brand}/lifecycle", { params: { path: { brand } }, body: { enabled: on } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    void load();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!products || !life) return <Loading />;
  // Moves name stages by key; show the names people gave them.
  const stageName = (level: string, key: string) => (key === "none" ? "no stage" : (life.stages.find((st) => st.level === level && st.key === key)?.name ?? key));

  return (
    <div className="space-y-6">
      <Card
        title="Lifecycle stages"
        description={
          life.enabled
            ? `On since ${stamp(life.enabled_at ?? "")}. Only events from then on move anyone.`
            : "Off. Turn them on to keep a stage per product for every company (billing) and person (usage)."
        }
        action={
          canManage && (
            <span className="flex flex-wrap gap-2">
              {life.stages.length > 0 && <Button variant="ghost" onClick={() => setEditing("stages")}>Edit stages</Button>}
              {life.stages.length > 0 && <Button variant="ghost" onClick={() => setEditing("moves")}>Edit moves</Button>}
              <Button variant={life.enabled ? "ghost" : "primary"} disabled={busy} onClick={() => toggle(!life.enabled)}>{life.enabled ? "Turn off" : "Turn on"}</Button>
            </span>
          )
        }
      >
        {life.stages.length > 0 ? (
          <div className="grid gap-4 md:grid-cols-2">
            {(["company", "person"] as const).map((level) => (
              <div key={level}>
                <div className="mb-1 text-[12px] font-medium text-muted-foreground">{level === "company" ? "Company (billing)" : "Person (usage)"}</div>
                <div className="flex flex-wrap gap-1.5">
                  {life.stages
                    .filter((s) => s.level === level)
                    .map((s) => <Badge key={s.key} tone={categoryTone[s.category]} title={`Category ${s.category}, rank ${s.rank}`}>{s.name}</Badge>)}
                </div>
                <ul className="mt-2 space-y-0.5 text-[12px] text-muted-foreground">
                  {life.moves
                    .filter((m) => m.level === level)
                    .map((m) => (
                      <li key={m.key}>
                        {m.name}: {m.any_stage ? "any stage" : (m.from_stages ?? []).map((k) => stageName(level, k)).join(", ")} → {stageName(level, m.to_stage)} <span className="font-mono">({m.emit_event})</span>
                      </li>
                    ))}
                </ul>
              </div>
            ))}
          </div>
        ) : (
          <p className="text-[13px] text-muted-foreground">Turning stages on adds the default stages and moves, which you can then rename, add to and change here.</p>
        )}
        {editing === "stages" && <StagesSheet brand={brand} life={life} onClose={() => setEditing(null)} onSaved={(l) => { setLife(l); setEditing(null); }} />}
        {editing === "moves" && <MovesSheet brand={brand} life={life} onClose={() => setEditing(null)} onSaved={(l) => { setLife(l); setEditing(null); }} />}
      </Card>

      {names.length > 0 && (
        <Card title="Product names waiting" description="Names your events or billing used that match no product. They drive nothing until you map them to a product or ignore them.">
          <ul className="divide-y">
            {names.map((n) => (
              <li key={n.id} className="flex flex-wrap items-center gap-3 py-2">
                <span className="font-mono text-[13px]">{n.value}</span>
                <Badge tone="grey">{n.source}</Badge>
                <span className="text-[12px] text-muted-foreground">{plural(n.seen_count, "time")}{n.last_seen_at ? `, last ${ago(n.last_seen_at)}` : ""}</span>
                {canManage && (
                  <select aria-label={`Map ${n.value}`} className={`${field} ml-auto`} disabled={busy} value="" onChange={(e) => e.target.value && mapName(n, e.target.value)}>
                    <option value="">Map to…</option>
                    {products.filter((p) => !p.archived_at).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                    <option value="ignore">Not a product (ignore)</option>
                  </select>
                )}
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card
        title={archived ? "Archived products" : "Products"}
        action={
          <span className="flex gap-2">
            <Button variant="ghost" onClick={() => setArchived(!archived)}>{archived ? "Show current" : "Show archived"}</Button>
            {canManage && !archived && <Button onClick={() => setAdding(!adding)}>Add a product</Button>}
          </span>
        }
      >
        {adding && (
          <div className="mb-4 flex flex-wrap items-end gap-2 rounded-md border p-3">
            <input aria-label="Name" className={`${field} w-56`} placeholder="Name, such as Residential proxies" value={name} onChange={(e) => setName(e.target.value)} />
            <input aria-label="Aliases" className={`${field} w-72`} placeholder="Aliases: resi, billing:prod_123" value={aliases} onChange={(e) => setAliases(e.target.value)} />
            <select aria-label="Churn" className={field} value={churnMode} onChange={(e) => setChurnMode(e.target.value as "cancellation" | "inactivity")}>
              <option value="cancellation">Churned when they cancel</option>
              <option value="inactivity">Churned after no usage</option>
            </select>
            <Button disabled={busy || !name.trim()} onClick={add}>Add</Button>
          </div>
        )}
        {products.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">
            {archived ? "No archived products." : <>No products yet. Events name theirs with <span className="font-mono">properties.product</span> (an alias) or <span className="font-mono">properties.product_id</span> (its id or key).</>}
          </p>
        ) : (
          <ul className="divide-y">
            {products.map((p) => (
              <li key={p.id} className="flex flex-wrap items-center gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="text-[13px] font-medium">{p.name} <span className="ml-1 font-mono text-[12px] text-muted-foreground">{p.key}</span></div>
                  <div className="text-[12px] text-muted-foreground">
                    {p.churn_mode === "inactivity" ? `Churned after ${p.churn_days} days without usage` : "Churned when they cancel"} · at risk after {p.at_risk_days} days without usage
                    {p.aliases.length > 0 && <> · {p.aliases.join(", ")}</>}
                  </div>
                </div>
                {canManage && <Button variant="ghost" disabled={busy} onClick={() => archive(p)}>{p.archived_at ? "Restore" : "Archive"}</Button>}
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

// RecordLifecycle is where a company or person stands with each product,
// and a company's overall status. It renders nothing when the record has
// no stage yet.
export function RecordLifecycle({ brand, record }: { brand: string; record: string }) {
  const [v, setV] = useState<RecordLifecycleView | null>(null);
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/records/{record}/lifecycle", { params: { path: { brand, record } } }).then(({ data }) => setV(data ?? null));
  }, [brand, record]);
  if (!v || v.states.length === 0) return null;
  return (
    <Card title="Products" description="Where this record stands with each product.">
      {v.overall && (
        <div className="mb-2 flex flex-wrap items-center gap-2 text-[13px]">
          <span className="text-muted-foreground">Overall</span>
          <Badge tone="blue">{v.overall.status}</Badge>
          {v.overall.flags.map((f) => <Badge key={f} tone="amber">{f.replace("_", " ")}</Badge>)}
        </div>
      )}
      <ul className="space-y-1 text-[13px]">
        {v.states.map((s) => (
          <li key={s.product_id + s.level} className="flex flex-wrap items-center gap-2">
            <span className="font-mono">{s.product_key}</span>
            <span className="text-[12px] text-muted-foreground">{s.level === "company" ? "billing" : "usage"}</span>
            <Badge tone={categoryTone[s.category ?? ""] ?? "grey"}>{s.stage}</Badge>
            <span className="text-[12px] text-muted-foreground" title={stamp(s.entered_at)}>since {ago(s.entered_at)}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

type RecordMetricValue = Schemas["RecordMetricValue"];

// RecordMetrics is a company's or person's live metrics (docs/metrics.md),
// overall and per product. It renders nothing when they have none.
export function RecordMetrics({ brand, record }: { brand: string; record: string }) {
  const [list, setList] = useState<RecordMetricValue[] | null>(null);
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/records/{record}/metrics", { params: { path: { brand, record } } }).then(({ data }) => setList(data?.data ?? null));
  }, [brand, record]);
  if (!list || list.length === 0) return null;
  const show = (v: unknown) => (typeof v === "number" ? v.toLocaleString() : typeof v === "string" ? ago(v) : "–");
  const overall = list.filter((m) => !m.product_id);
  return (
    <Card title="Metrics" description="Live totals over a rolling window, per product below each.">
      <dl className="grid grid-cols-[minmax(0,3fr)_minmax(0,2fr)] gap-x-3 gap-y-1 text-[13px]">
        {overall.map((m) => (
          <Fragment key={m.metric_id}>
            <dt className="font-mono text-muted-foreground">{m.key}</dt>
            <dd className="text-right tabular-nums">
              {show(m.value)}
              {list
                .filter((p) => p.metric_id === m.metric_id && p.product_id)
                .map((p) => (
                  <div key={p.product_id} className="text-[11px] text-muted-foreground">
                    {p.product_key}: {show(p.value)}
                  </div>
                ))}
            </dd>
          </Fragment>
        ))}
      </dl>
    </Card>
  );
}
