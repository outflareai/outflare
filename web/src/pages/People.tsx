import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { Search } from "lucide-react";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { DataTable } from "@/components/data-table";
import { BrandBadge } from "@/components/brand-badge";
import { PickBrand } from "@/components/pick-brand";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useBrand } from "@/lib/brand";
import { isID } from "@/lib/paths";
import { ago, stamp, when } from "../format";
import type { Tab } from "../App";
import { displayName, fieldLabelValue, identity, nameItem, namespaces, originPhrases, origins, useCatalog, type CatalogInfo } from "@/lib/records";
import { AddIdentity, eraseRecord, RecordConsent, RecordDialog, softDeleteEvent, softDeleteRecord } from "./RecordEdit";
import { Duplicates } from "./Duplicates";
import { BrandEvents } from "./BrandEvents";
import { Products, RecordLifecycle, RecordMetrics } from "./Products";
import { RecentlyDeleted } from "./RecentlyDeleted";
import { Batches, RecordMerges } from "./Undo";
import { RecordPartners } from "./Partners";
import { RecordTransactions, Transactions } from "./Transactions";
import { Metrics } from "./Metrics";
import { BulkEditSheet } from "./BulkEdit";

type Rec = Schemas["Record"];
type Field = Schemas["Field"];
type TimelineItem = Schemas["TimelineItem"];
type Change = Schemas["AttributeChange"];
type Identity = Schemas["RecordIdentity"];
type Link = Schemas["RecordRelationship"];
type Consent = Schemas["Consent"];
type PrivacyExport = Schemas["PrivacyExport"];

// People and Companies are the two built-in record types; the item part of
// the URL picks the view: "" for people, "companies", "duplicates", "events"
// (the brand's product events as received), "products" (its products and
// lifecycle stages), "batches" (imports and sends
// of events, each undoable in one step), "deleted" (Recently deleted,
// restorable for 7 days), or a record's id.
type View = "person" | "account";
const viewOf = (open: string): View => (open === "companies" ? "account" : "person");
const itemOf = (v: View) => (v === "account" ? "companies" : "");





export function People({ ws, canManage, canConfigure = false, open, onOpen, go }: { ws: string; canManage: boolean; canConfigure?: boolean; open: string; onOpen: (item: string) => void; go: (t: Tab) => void }) {
  const cat = useCatalog(ws);
  if (isID(open)) return <RecordPage key={open} ws={ws} id={open} cat={cat} canManage={canManage} onOpen={onOpen} go={go} />;
  const tabs = <ViewTabs value={open === "duplicates" || open === "events" || open === "products" || open === "batches" || open === "deleted" || open === "transactions" || open === "metrics" ? open : viewOf(open)} onOpen={onOpen} />;
  if (open === "duplicates") return <Duplicates ws={ws} cat={cat} canManage={canManage} tabs={tabs} onOpen={onOpen} />;
  if (open === "events") return <BrandEvents ws={ws} canManage={canManage} tabs={tabs} onOpen={onOpen} />;
  // Products and lifecycle stages are part of the catalog: catalog.manage.
  if (open === "products") return <Products ws={ws} canManage={canConfigure} tabs={tabs} />;
  if (open === "metrics") return <Metrics ws={ws} canManage={canConfigure} tabs={tabs} />;
  if (open === "transactions") return <Transactions ws={ws} canManage={canManage} tabs={tabs} onOpen={onOpen} />;
  if (open === "batches") return <Batches ws={ws} canManage={canManage} tabs={tabs} onOpen={onOpen} />;
  if (open === "deleted") return <RecentlyDeleted ws={ws} canManage={canManage} tabs={tabs} onOpen={onOpen} />;
  return <RecordList ws={ws} view={viewOf(open)} cat={cat} canManage={canManage} tabs={tabs} onOpen={onOpen} />;
}

function ViewTabs({ value, onOpen }: { value: View | "duplicates" | "events" | "products" | "batches" | "deleted" | "transactions" | "metrics"; onOpen: (item: string) => void }) {
  return (
    <Tabs value={value} onValueChange={(v) => onOpen(v === "duplicates" || v === "events" || v === "products" || v === "batches" || v === "deleted" || v === "transactions" || v === "metrics" ? v : itemOf(v as View))}>
      <TabsList className="h-8">
        <TabsTrigger value="person" className="px-3 text-[12px]">People</TabsTrigger>
        <TabsTrigger value="account" className="px-3 text-[12px]">Companies</TabsTrigger>
        <TabsTrigger value="duplicates" className="px-3 text-[12px]">Duplicates</TabsTrigger>
        <TabsTrigger value="events" className="px-3 text-[12px]">Events</TabsTrigger>
        <TabsTrigger value="products" className="px-3 text-[12px]">Products</TabsTrigger>
        <TabsTrigger value="transactions" className="px-3 text-[12px]">Transactions</TabsTrigger>
        <TabsTrigger value="metrics" className="px-3 text-[12px]">Metrics</TabsTrigger>
        <TabsTrigger value="batches" className="px-3 text-[12px]">Imports</TabsTrigger>
        <TabsTrigger value="deleted" className="px-3 text-[12px]">Recently deleted</TabsTrigger>
      </TabsList>
    </Tabs>
  );
}



const dash = <span className="text-muted-foreground">–</span>;

function RecordList({ ws, view, cat, canManage, tabs, onOpen }: { ws: string; view: View; cat: CatalogInfo; canManage: boolean; tabs: ReactNode; onOpen: (item: string) => void }) {
  const [rows, setRows] = useState<Rec[]>([]);
  const [adding, setAdding] = useState(false);
  const [next, setNext] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [q, setQ] = useState("");
  const [show, setShow] = useState<"active" | "review" | "archived">("active");
  // Field filters: one value per field key.
  const [filters, setFilters] = useState<Record<string, string>>({});
  // Records ticked for a bulk edit (docs/bulk-edits.md).
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [bulk, setBulk] = useState<string[] | null>(null);
  const { brand, active } = useBrand(ws);
  const brandID = brand?.id ?? "";

  // The fields worth filtering by: the catalog marks them, and a choice
  // from a list fits a dropdown.
  const filterFields = cat.fields(view).filter((f) => f.filterable && (f.value_type === "select" || f.value_type === "status") && f.option_set);
  const filterKey = JSON.stringify(filters);

  const load = useCallback(
    async (cursor?: string) => {
      if (!brandID) return;
      const attribute = Object.entries(filters).filter(([, v]) => v).map(([k, v]) => `${k}:${v}`);
      const { data, error } = await api.GET("/v1/brands/{brand}/records", {
        params: {
          path: { brand: brandID },
          query: {
            type: view,
            limit: 100,
            ...(q.trim() ? { q: q.trim() } : {}),
            ...(attribute.length ? { attribute } : {}),
            ...(show === "review" ? { review: "needs_review" as const } : {}),
            ...(show === "archived" ? { archived: "true" as const } : {}),
            ...(cursor ? { cursor } : {}),
          },
        },
      });
      setLoaded(true);
      if (!data) return setError(errorMessage(error));
      setError("");
      setRows((prev) => (cursor ? [...prev, ...data.data] : data.data));
      setNext(data.next_cursor ?? null);
    },
    [brandID, view, q, show, filterKey], // eslint-disable-line react-hooks/exhaustive-deps
  );

  useEffect(() => {
    const t = setTimeout(() => load(), 200);
    return () => clearTimeout(t);
  }, [load]);

  // Switching between people and companies starts the filters again.
  useEffect(() => {
    setFilters({});
    setPicked(new Set());
    if (view === "account") nameItem("companies", "Companies");
  }, [view]);

  const label = (f: string, v: unknown) => fieldLabelValue(cat, view, f, v);
  const filtered = !!q || show !== "active" || Object.values(filters).some(Boolean);
  const people = view === "person";
  // Bulk edits change live records only, so archived and held ones can't be picked.
  const canBulk = canManage && show === "active";
  const toggle = (id: string) =>
    setPicked((p) => {
      const n = new Set(p);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <div className="space-y-6">
      <PageHeader
        title={people ? "People" : "Companies"}
        description={
          people
            ? "Everyone your business knows: customers from your product, leads, CRM contacts and the people who reply. One record each, shared by every feature."
            : "The companies your people work at, from your product, your CRM, leads' websites and imports."
        }
        action={canManage && cat.catalog && brandID && <Button onClick={() => setAdding(true)}>{people ? "Add person" : "Add company"}</Button>}
      />
      {adding && <RecordDialog ws={ws} brand={brandID} type={view} rec={null} cat={cat} names={{}} onClose={() => setAdding(false)} onSaved={(r) => onOpen(r.id)} />}
      {bulk && <BulkEditSheet ws={ws} brand={brandID} source={{ kind: "records", ids: bulk, type: view }} onClose={() => setBulk(null)} onDone={() => { setBulk(null); setPicked(new Set()); void load(); }} />}
      {tabs}
      {!brandID && <PickBrand what={people ? "People" : "Companies"} brands={active.length} />}
      <ErrorText>{brandID && (error || cat.error)}</ErrorText>
      {brandID && !loaded && <Loading />}
      {brandID && loaded && !filtered && rows.length === 0 && (
        <Empty title={people ? "No people yet" : "No companies yet"}>
          {people
            ? "People arrive from your product (sign-ups and payments), leads you import, your CRM and the replies you get. Connect a source or import leads to start."
            : "Companies arrive with your people: from their email domains, your CRM and leads' websites."}
        </Empty>
      )}
      {brandID && loaded && (filtered || rows.length > 0) && (
        <div className="space-y-3">
          <DataTable
            data={rows}
            rowId={(r) => r.id}
            onRowClick={(r) => onOpen(r.id)}
            toolbar={
              <>
                <label className="relative min-w-0 flex-1 basis-60">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <input
                    type="search"
                    aria-label={people ? "Search people" : "Search companies"}
                    className={`${field} w-full pl-8`}
                    placeholder={people ? "Search name, email, phone or id" : "Search name or domain"}
                    value={q}
                    onChange={(e) => setQ(e.target.value)}
                  />
                </label>
                {filterFields.map((f) => (
                  <select key={f.key} aria-label={f.label} className={`${field} max-w-44`} value={filters[f.key] ?? ""} onChange={(e) => setFilters({ ...filters, [f.key]: e.target.value })}>
                    <option value="">Any {f.label.toLowerCase()}</option>
                    {cat.options(f).map((o) => <option key={o.key} value={o.key}>{o.label}</option>)}
                  </select>
                ))}
                {canBulk && rows.length > 0 && (
                  <Button variant="ghost" onClick={() => setBulk(picked.size ? [...picked] : rows.map((r) => r.id))}>
                    {picked.size ? `Bulk edit ${picked.size} picked` : `Bulk edit the ${rows.length} shown`}
                  </Button>
                )}
                <select aria-label="Show" className={field} value={show} onChange={(e) => setShow(e.target.value as typeof show)}>
                  <option value="active">Active</option>
                  <option value="review">Held for review</option>
                  <option value="archived">Archived</option>
                </select>
              </>
            }
            empty={<>Nothing matches. <button className="underline" onClick={() => { setQ(""); setFilters({}); setShow("active"); }}>Clear the filters</button></>}
            mobile={(r) => (
              <div className="min-w-0">
                <div className="font-medium">{displayName(r)}</div>
                <div className="text-xs text-muted-foreground">
                  {(people ? [identity(r, "email"), r.attributes.job_title as string] : [identity(r, "domain"), label("industry", r.attributes.industry)]).filter(Boolean).join(" · ")}
                </div>
              </div>
            )}
            hidden={["origin"]}
            columns={[
              ...(canBulk
                ? [
                    {
                      id: "pick",
                      header: "",
                      enableSorting: false,
                      enableHiding: false,
                      cell: ({ row: { original: r } }: { row: { original: Rec } }) => (
                        <input type="checkbox" aria-label={`Pick ${displayName(r)}`} checked={picked.has(r.id)} onClick={(e) => e.stopPropagation()} onChange={() => toggle(r.id)} />
                      ),
                    },
                  ]
                : []),
              {
                id: "name",
                header: "Name",
                accessorFn: (r) => displayName(r),
                cell: ({ row: { original: r } }) => (
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <span className="font-medium">{displayName(r)}</span>
                    {r.needs_review && <Badge tone="amber" title="Held for someone to approve before anything is sent to them.">Needs review</Badge>}
                    {r.archived && <Badge>Archived</Badge>}
                    <BrandBadge id={r.brand_id} />
                  </span>
                ),
              },
              people
                ? { id: "email", header: "Email", accessorFn: (r) => identity(r, "email") ?? "", cell: ({ getValue }) => (getValue() as string) ? <span className="font-mono text-[12px]">{getValue() as string}</span> : dash }
                : { id: "domain", header: "Domain", accessorFn: (r) => identity(r, "domain") ?? "", cell: ({ getValue }) => (getValue() as string) ? <span className="font-mono text-[12px]">{getValue() as string}</span> : dash },
              ...(people
                ? [
                    { id: "job_title", header: "Job title", accessorFn: (r: Rec) => (r.attributes.job_title as string) ?? "", cell: ({ getValue }: { getValue: () => unknown }) => (getValue() as string) || dash },
                    { id: "seniority", header: "Seniority", accessorFn: (r: Rec) => label("seniority", r.attributes.seniority), cell: ({ getValue }: { getValue: () => unknown }) => (getValue() as string) || dash },
                  ]
                : [
                    { id: "industry", header: "Industry", accessorFn: (r: Rec) => label("industry", r.attributes.industry), cell: ({ getValue }: { getValue: () => unknown }) => (getValue() as string) || dash },
                    { id: "company_size", header: "Size", accessorFn: (r: Rec) => label("company_size", r.attributes.company_size), cell: ({ getValue }: { getValue: () => unknown }) => (getValue() as string) || dash },
                  ]),
              { id: "lifecycle_stage", header: "Stage", accessorFn: (r) => label("lifecycle_stage", r.attributes.lifecycle_stage), cell: ({ getValue }) => (getValue() as string) ? <Badge>{getValue() as string}</Badge> : dash },
              { id: "origin", header: "From", accessorFn: (r) => origins[r.origin], cell: ({ getValue }) => <span className="text-muted-foreground">{getValue() as string}</span> },
              { id: "created", header: "Added", accessorFn: (r) => r.created_at, cell: ({ row: { original: r } }) => <span className="text-muted-foreground" title={stamp(r.created_at)}>{ago(r.created_at)}</span> },
            ]}
          />
          {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
        </div>
      )}
    </div>
  );
}


// FieldValue shows a value the way its field's type reads.
function FieldValue({ f, v, cat, names, onOpen }: { f?: Field; v: unknown; cat: CatalogInfo; names: Record<string, string>; onOpen: (id: string) => void }): ReactNode {
  if (v === null || v === undefined || v === "" || (Array.isArray(v) && v.length === 0)) return dash;
  if (Array.isArray(v)) {
    return (
      <span className="inline-flex flex-wrap gap-x-2 gap-y-1">
        {v.map((x, i) => <span key={i}><FieldValue f={f} v={x} cat={cat} names={names} onOpen={onOpen} />{i < v.length - 1 && ","}</span>)}
      </span>
    );
  }
  switch (f?.value_type) {
    case "select":
    case "status":
      return <>{(f.option_set && cat.labels.get(f.option_set)?.get(String(v))) ?? String(v)}</>;
    case "checkbox":
      return <>{v ? "Yes" : "No"}</>;
    case "currency": {
      const c = v as { amount_minor?: number; currency?: string };
      if (typeof c.amount_minor !== "number" || !c.currency) return <>{JSON.stringify(v)}</>;
      try {
        return <>{new Intl.NumberFormat(undefined, { style: "currency", currency: c.currency }).format(c.amount_minor / 100)}</>;
      } catch {
        return <>{(c.amount_minor / 100).toFixed(2)} {c.currency}</>;
      }
    }
    case "timestamp":
      return <span title={String(v)}>{stamp(String(v))}</span>;
    case "rating":
      return <span title={`${v} of 5`}>{"★".repeat(Number(v))}{"☆".repeat(Math.max(0, 5 - Number(v)))}</span>;
    case "url":
      return /^https?:\/\//i.test(String(v)) ? <a className="break-all underline" href={String(v)} target="_blank" rel="noreferrer noopener">{String(v).replace(/^https?:\/\/(www\.)?/i, "").replace(/\/$/, "")}</a> : <>{String(v)}</>;
    case "email":
      return <a className="break-all underline" href={`mailto:${String(v)}`}>{String(v)}</a>;
    case "location": {
      const l = v as Record<string, string>;
      return <>{[l.city, l.region, l.country].filter(Boolean).join(", ") || JSON.stringify(v)}</>;
    }
    case "record_reference":
      return <button type="button" className="underline" onClick={() => onOpen(String(v))}>{names[String(v)] ?? "Open record"}</button>;
    case "actor_reference":
      return <>{names[String(v)] ?? "A former member"}</>;
  }
  return <span className="break-words">{typeof v === "object" ? JSON.stringify(v) : String(v)}</span>;
}

function RecordPage({ ws, id, cat: picked, canManage, onOpen, go }: { ws: string; id: string; cat: CatalogInfo; canManage: boolean; onOpen: (item: string) => void; go: (t: Tab) => void }) {
  const [rec, setRec] = useState<Rec | null>(null);
  // A record's fields are its brand's catalog's, whichever brand is picked.
  const own = useCatalog(ws, rec?.brand_id);
  const cat = rec?.brand_id ? own : picked;
  const [links, setLinks] = useState<Link[]>([]);
  // Workspace members by user id, so owner and other member fields read
  // as names. Without members.read the field shows a placeholder.
  const [members, setMembers] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [section, setSection] = useState<"timeline" | "history">("timeline");
  const [empty, setEmpty] = useState(false);
  const [editing, setEditing] = useState(false);
  // Goes up after every change, so the cards read the record again.
  const [tick, setTick] = useState(0);
  const changed = () => setTick((t) => t + 1);
  const [twins, setTwins] = useState<Rec[]>([]);
  // What every source says about each field, and the hand edits behind
  // its values (docs/customers.md, "Where a value comes from").
  const [sources, setSources] = useState<Schemas["FieldSourceValue"][]>([]);
  const [handEdits, setHandEdits] = useState<Schemas["HandEdit"][]>([]);
  // A record is opened inside the brand picked: brand routes only.
  const { brand: pickedBrand, active } = useBrand(ws);
  const brand = pickedBrand?.id ?? "";

  useEffect(() => {
    if (!brand) return;
    const path = { brand, record: id };
    api.GET("/v1/brands/{brand}/records/{record}", { params: { path } }).then(({ data, error }) => {
      if (!data) return setError(errorMessage(error));
      setRec(data);
      window.dispatchEvent(new CustomEvent("outflare:item-name", { detail: { id, name: displayName(data) } }));
    });
    api.GET("/v1/brands/{brand}/records/{record}/relationships", { params: { path } }).then(({ data }) => data && setLinks(data.data));
    api.GET("/v1/brands/{brand}/records/{record}/field-values", { params: { path } }).then(({ data }) => {
      setSources(data?.data ?? []);
      setHandEdits(data?.hand_edits ?? []);
    });
    api.GET("/v1/brands/{brand}/records/duplicates", { params: { path: { brand }, query: { record: id, status: "open,blocked" } } }).then(({ data }) =>
      setTwins((data?.data ?? []).flatMap((p) => p.records.filter((r) => r.id !== id))),
    );
  }, [brand, id, tick]);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) =>
      setMembers(Object.fromEntries((data?.data ?? []).map((m) => [m.user_id, m.name || m.email]))),
    );
  }, [ws]);

  if (!brand) return <div className="space-y-6"><PageHeader title="Record" back={{ label: "People", onClick: () => onOpen("") }} /><PickBrand what="Records" brands={active.length} /></div>;
  if (error) return <div className="space-y-6"><PageHeader title="Record" back={{ label: "People", onClick: () => onOpen("") }} /><ErrorText>{error}</ErrorText></div>;
  if (!rec) return <Loading />;

  const people = rec.type === "person";
  const list = rec.type === "account" ? "companies" : "";
  const fields = cat.fields(rec.type);
  // Names of the records this one points at, from its links.
  const names: Record<string, string> = { ...members, ...Object.fromEntries(links.map((l) => [l.record.id, l.record.name || "Unnamed"])), [rec.id]: displayName(rec) };
  const known = new Set(fields.map((f) => f.key));
  const has = (v: unknown) => v !== null && v !== undefined && v !== "" && !(Array.isArray(v) && v.length === 0);
  const blank = fields.filter((f) => !has(rec.attributes[f.key]));
  // metrics are live totals (docs/metrics.md), shown in their own card.
  const extra = Object.keys(rec.attributes).filter((k) => !known.has(k) && k !== "metrics");
  const manage = canManage && !rec.merged_into;

  async function archive(archived: boolean) {
    const { error } = await api.PATCH("/v1/brands/{brand}/records/{record}", { params: { path: { brand, record: rec!.id } }, body: { archived, version: rec!.version } });
    if (error) return toast(errorMessage(error), "error");
    toast(archived ? "Archived. It's under Archived in the list, and can be restored." : "Restored.");
    changed();
  }

  async function review(decision: "approve" | "reject") {
    if (decision === "reject" && !(await confirmAction(`Reject ${displayName(rec!)}?`, "They are archived, and nothing is sent to them.", "Reject"))) return;
    const { error } = await api.POST("/v1/brands/{brand}/records/{record}/review", { params: { path: { brand, record: rec!.id } }, body: { decision } });
    if (error) return toast(errorMessage(error), "error");
    toast(decision === "approve" ? "Approved. They can now be emailed as a prospect (cold email only, never marketing without consent)." : "Rejected.");
    changed();
  }

  // Use this: another source's value, saved as a person's own edit.
  async function useValue(key: string, value: unknown) {
    const { error } = await api.PATCH("/v1/brands/{brand}/records/{record}", { params: { path: { brand, record: rec!.id } }, body: { attributes: { [key]: value as never }, version: rec!.version } });
    if (error) return toast(errorMessage(error), "error");
    toast("Saved as your edit.");
    changed();
  }

  async function pin(key: string, pinned: boolean) {
    const { error } = await api.POST("/v1/brands/{brand}/records/{record}/fields/{field}/pin", { params: { path: { brand, record: rec!.id, field: key } }, body: { pinned } });
    if (error) return toast(errorMessage(error), "error");
    toast(pinned ? "Pinned: it wins over every other source for good." : "Unpinned: its time runs again from now.");
    changed();
  }

  async function erase() {
    if (await eraseRecord(rec!, displayName(rec!))) onOpen(list);
  }

  async function softDelete() {
    if (await softDeleteRecord(rec!, displayName(rec!))) onOpen(list);
  }

  return (
    <div className="space-y-6">
      <PageHeader
        back={rec.type === "person" || rec.type === "account" ? { label: people ? "People" : "Companies", onClick: () => onOpen(list) } : undefined}
        title={
          <>
            {displayName(rec)}
            {rec.needs_review && <Badge tone="amber" title="Held for someone to approve before anything is sent to them.">Needs review</Badge>}
            {rec.archived && <Badge>Archived</Badge>}
            <BrandBadge id={rec.brand_id} />
          </>
        }
        description={
          <>
            {cat.catalog?.types.find((t) => t.key === rec.type)?.name ?? rec.type} {originPhrases[rec.origin]}
            {rec.contactability?.acquired_via ? ` (${rec.contactability.acquired_via})` : ""}, added {ago(rec.created_at)}, changed {ago(rec.updated_at)}.
          </>
        }
        action={
          manage && (
            <>
              {rec.needs_review && <Button onClick={() => review("approve")}>Approve</Button>}
              {rec.needs_review && <Button variant="ghost" onClick={() => review("reject")}>Reject</Button>}
              <Button variant={rec.needs_review ? "ghost" : "primary"} onClick={() => setEditing(true)}>Edit</Button>
              <Menu
                aria={`More actions for ${displayName(rec)}`}
                items={[
                  rec.archived ? { label: "Restore", onClick: () => archive(false) } : { label: "Archive", onClick: () => archive(true) },
                  ...(rec.brand_id ? [{ label: "Delete…", onClick: softDelete, danger: true }] : []),
                  { label: "Erase permanently…", onClick: erase, danger: true },
                ]}
              />
            </>
          )
        }
      />
      {editing && <RecordDialog ws={ws} brand={brand} type={rec.type} rec={rec} cat={cat} names={names} onClose={() => setEditing(false)} onSaved={() => changed()} />}
      {twins.length > 0 && !rec.merged_into && (
        <p className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
          This may be the same {people ? "person" : "company"} as{" "}
          {twins.map((t, i) => (
            <span key={t.id}>
              {i > 0 && ", "}
              <button className="underline" onClick={() => onOpen(t.id)}>{displayName(t)}</button>
            </span>
          ))}
          . <button className="underline" onClick={() => onOpen("duplicates")}>Review duplicates</button>
        </p>
      )}
      {rec.merged_into && (
        <p className="rounded-md border bg-muted/40 p-3 text-sm">
          This record was merged into another. <button className="underline" onClick={() => onOpen(rec.merged_into!)}>Open the record it was merged into</button>
        </p>
      )}
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="space-y-6 lg:col-span-1">
          <Card title="Fields" description={canManage ? undefined : "From the catalog of fields this workspace keeps."}>
            <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-3 gap-y-2 text-sm">
              {fields.filter((f) => empty || has(rec.attributes[f.key])).map((f) => (
                <FieldRow key={f.key} label={f.label} title={[f.description, rec.attribute_sources[f.key] && `Set by ${rec.attribute_sources[f.key]}`].filter(Boolean).join(" · ")}>
                  <FieldValue f={f} v={rec.attributes[f.key]} cat={cat} names={names} onOpen={onOpen} />
                  <FieldSources
                    f={f}
                    others={sources.filter((v) => v.field === f.key && !v.current && !(v.source === "manual" && rec.attribute_sources[f.key] === "manual"))}
                    edit={rec.attribute_sources[f.key] === "manual" ? handEdits.find((h) => h.field === f.key) : undefined}
                    cat={cat}
                    names={names}
                    onOpen={onOpen}
                    manage={manage}
                    onUse={useValue}
                    onPin={pin}
                  />
                  {manage && f.write_policy !== "computed" && has(rec.attributes[f.key]) && (
                    <button className="ml-2 text-xs text-muted-foreground underline hover:text-foreground" aria-label={`Delete the value of ${f.label}`} onClick={async () => (await deleteFromRecord(rec, { kind: "field", key: f.key, label: f.label })) && changed()}>
                      Delete value
                    </button>
                  )}
                </FieldRow>
              ))}
              {extra.map((k) => (
                <FieldRow key={k} label={k} title="Not in the catalog any more.">
                  <FieldValue v={rec.attributes[k]} cat={cat} names={names} onOpen={onOpen} />
                </FieldRow>
              ))}
            </dl>
            {blank.length > 0 && <button className="mt-3 text-xs underline" onClick={() => setEmpty(!empty)}>{empty ? "Hide empty fields" : `Show ${blank.length} empty fields`}</button>}
          </Card>
          {rec.contactability && <Contact c={rec.contactability} />}
          <Identities key={`i${tick}`} rec={rec} canManage={manage} onChange={changed} />
          <Links rec={rec} links={links} fields={fields} canManage={manage} onChange={changed} onOpen={onOpen} />
          {people && <Consents key={`c${tick}`} brand={brand} id={id} canManage={manage} onChange={changed} go={go} />}
          {people && <div className="rounded-md border p-4 text-sm empty:hidden"><RecordPartners ws={ws} record={id} go={() => go("partners")} /></div>}
          {rec.brand_id && <RecordLifecycle key={`l${tick}`} brand={rec.brand_id} record={id} />}
          {rec.brand_id && <RecordMetrics key={`x${tick}`} brand={rec.brand_id} record={id} />}
          {rec.brand_id && <RecordTransactions key={`t${tick}`} brand={rec.brand_id} record={id} />}
          {rec.brand_id && !rec.merged_into && <PrivacyExports key={`p${tick}`} brand={rec.brand_id} record={id} canManage={canManage} />}
          {rec.brand_id && <RecordMerges key={`m${tick}`} brand={rec.brand_id} record={id} canManage={canManage} onOpen={onOpen} onChange={changed} />}
        </div>
        <div className="space-y-4 lg:col-span-2">
          <Tabs value={section} onValueChange={(v) => setSection(v as typeof section)}>
            <TabsList className="h-8">
              <TabsTrigger value="timeline" className="px-3 text-[12px]">Timeline</TabsTrigger>
              <TabsTrigger value="history" className="px-3 text-[12px]">Field history</TabsTrigger>
            </TabsList>
          </Tabs>
          {section === "timeline" && <Timeline key={tick} brand={brand} rec={rec} cat={cat} names={names} onOpen={onOpen} canManage={manage} />}
          {section === "history" && <History key={tick} brand={brand} rec={rec} cat={cat} names={names} onOpen={onOpen} />}
        </div>
      </div>
    </div>
  );
}

// FieldSources shows, under a field's value, what the other sources say
// (with Use this) and, for a value set by hand, how long it wins (with Pin).
function FieldSources({ f, others, edit, cat, names, onOpen, manage, onUse, onPin }: {
  f: Field; others: Schemas["FieldSourceValue"][]; edit?: Schemas["HandEdit"]; cat: CatalogInfo; names: Record<string, string>; onOpen: (id: string) => void;
  manage: boolean; onUse: (key: string, value: unknown) => void; onPin: (key: string, pinned: boolean) => void;
}) {
  if (others.length === 0 && !edit) return null;
  const until = edit && (edit.pinned ? "Pinned: wins for good" : edit.expired ? "Its time ran out: the next source to write it replaces it" : edit.expires_at ? `Your edit wins until ${new Date(edit.expires_at).toLocaleDateString()}` : "Your edit wins for good");
  return (
    <div className="mt-1 space-y-0.5 text-[12px] text-muted-foreground">
      {until && (
        <div className="flex flex-wrap items-center gap-x-2">
          <span>{until}</span>
          {manage && <button type="button" className="underline" onClick={() => onPin(f.key, !edit!.pinned)}>{edit!.pinned ? "Unpin" : "Pin"}</button>}
        </div>
      )}
      {others.map((o) => (
        <div key={o.source} className="flex flex-wrap items-center gap-x-2">
          <span>{sourceLabel(o.source)} says</span>
          <span className="min-w-0 text-foreground/80"><FieldValue f={f} v={o.value} cat={cat} names={names} onOpen={onOpen} /></span>
          {manage && <button type="button" className="underline" onClick={() => onUse(f.key, o.value)}>Use this</button>}
        </div>
      ))}
    </div>
  );
}

const sourceLabels: Record<string, string> = { manual: "A person", first_party: "Your product", crm_sync: "Your CRM", enrichment: "Enrichment", import: "An import" };
const sourceLabel = (s: string) => sourceLabels[s] ?? s;

function FieldRow({ label, title, children }: { label: string; title?: string; children: ReactNode }) {
  return (
    <>
      <dt className="truncate text-muted-foreground" title={title || undefined}>{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

const bases: Record<Schemas["Contactability"]["basis"], { label: string; tone: "green" | "amber" | "red" | "grey" }> = {
  opted_in: { label: "Opted in", tone: "green" },
  opted_out: { label: "Opted out", tone: "red" },
  customer: { label: "Customer", tone: "green" },
  third_party: { label: "Prospect", tone: "amber" },
  unknown: { label: "Unknown basis", tone: "grey" },
};

// What may go out to them, and why.
function Contact({ c }: { c: Schemas["Contactability"] }) {
  const allowed = [["transactional", "Account email"], ["cold_outreach", "Cold email"], ["marketing", "Marketing"]] as const;
  return (
    <Card title="Contact" description={c.why}>
      <div className="flex flex-wrap gap-1.5 text-sm">
        <Badge tone={bases[c.basis].tone}>{bases[c.basis].label}</Badge>
        {allowed.map(([k, label]) => <Badge key={k} tone={c.allowed[k] ? "green" : "grey"} title={c.allowed[k] ? "Allowed" : "Not allowed"}>{c.allowed[k] ? "✓" : "✕"} {label}</Badge>)}
        {c.suppressed.length > 0 && <Badge tone="red" title="On the do-not-contact list">Do not contact</Badge>}
      </div>
    </Card>
  );
}

// deleteFromRecord deletes one field value, email or id, or link into
// Recently deleted (docs/customers.md, "Deleting and restoring"): hidden at
// once, restorable for 7 days without firing anything. "Remove" on an
// email or id is different: it closes it and keeps it in the history.
async function deleteFromRecord(rec: Rec, what: { kind: "field"; key: string; label: string } | { kind: "identity" | "link"; id: string; label: string }): Promise<boolean> {
  const brand = rec.brand_id ?? "";
  const name = what.kind === "field" ? `the value of ${what.label}` : what.label;
  const ok = await confirmAction(
    `Delete ${name}?`,
    <p>It is hidden everywhere at once, and goes to People → Recently deleted, where you can restore it for 7 days without firing any rule or journey. Use this for wrong data.</p>,
    "Delete",
    true,
  );
  if (!ok) return false;
  const body = { version: rec.version };
  const { error } =
    what.kind === "field"
      ? await api.POST("/v1/brands/{brand}/records/{record}/fields/{field}/delete", { params: { path: { brand, record: rec.id, field: what.key } }, body })
      : what.kind === "identity"
        ? await api.POST("/v1/brands/{brand}/records/{record}/identities/{identity}/delete", { params: { path: { brand, record: rec.id, identity: what.id } }, body })
        : await api.POST("/v1/brands/{brand}/records/{record}/relationships/{relationship}/delete", { params: { path: { brand, record: rec.id, relationship: what.id } }, body });
  if (error) {
    toast(errorMessage(error), "error");
    return false;
  }
  toast(`Deleted. Restore it from Recently deleted within 7 days.`);
  return true;
}

function Identities({ rec, canManage, onChange }: { rec: Rec; canManage: boolean; onChange: () => void }) {
  const id = rec.id;
  const brand = rec.brand_id ?? "";
  const [list, setList] = useState<Identity[] | null>(null);
  const [past, setPast] = useState(false);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/records/{record}/identities", { params: { path: { brand, record: id }, query: { closed: true } } }).then(({ data }) => setList(data?.data ?? []));
  }, [brand, id]);
  if (!list) return null;
  const active = list.filter((i) => !i.valid_to);
  async function remove(i: Identity) {
    if (!(await confirmAction(`Remove ${i.value}?`, "It stops pointing at this record and stays in its history as removed. Nothing else changes.", "Remove"))) return;
    const { error } = await api.POST("/v1/brands/{brand}/records/{record}/identities/{identity}/close", { params: { path: { brand, record: id, identity: i.id } } });
    if (error) return toast(errorMessage(error), "error");
    onChange();
  }
  const closed = list.filter((i) => i.valid_to);
  const row = (i: Identity) => (
    <li key={i.id} className="flex items-start justify-between gap-2">
      <div className="min-w-0">
        <div className={`break-all font-mono text-[12px] ${i.valid_to ? "text-muted-foreground line-through" : ""}`}>{i.value}</div>
        <div className="text-xs text-muted-foreground">
          {namespaces[i.namespace] ?? i.namespace} · from {i.source}
          {i.valid_to ? ` · ${i.closed_reason ?? "closed"} ${ago(i.valid_to)}` : ` · since ${ago(i.valid_from)}`}
        </div>
      </div>
      {!i.valid_to && (
        <div className="flex shrink-0 items-center gap-1.5">
          {i.verified ? <Badge tone="green" title={i.verified_at ? `Verified ${stamp(i.verified_at)}` : "Verified"}>Verified</Badge> : <Badge title="Unverified: it doesn't claim the address, so another record can.">Unverified</Badge>}
          {canManage && <button className="text-xs text-muted-foreground underline hover:text-foreground" aria-label={`Remove ${i.value}`} onClick={() => remove(i)}>Remove</button>}
          {canManage && <button className="text-xs text-muted-foreground underline hover:text-foreground" aria-label={`Delete ${i.value}`} onClick={async () => (await deleteFromRecord(rec, { kind: "identity", id: i.id, label: i.value })) && onChange()}>Delete</button>}
        </div>
      )}
    </li>
  );
  return (
    <Card title="Emails and ids" description="What points at this record. Only a verified one claims its value.">
      {active.length === 0 && <p className="text-sm text-muted-foreground">None.</p>}
      <ul className="space-y-2 text-sm">{active.map(row)}</ul>
      {closed.length > 0 && (
        <div className="mt-3 text-sm">
          <button className="text-xs underline" onClick={() => setPast(!past)}>{past ? "Hide" : "Show"} {closed.length} past</button>
          {past && <ul className="mt-2 space-y-2">{closed.map(row)}</ul>}
        </div>
      )}
      {canManage && <AddIdentity rec={rec} onAdded={onChange} />}
    </Card>
  );
}

function Links({ rec, links, fields, canManage, onChange, onOpen }: { rec: Rec; links: Link[]; fields: Field[]; canManage: boolean; onChange: () => void; onOpen: (item: string) => void }) {
  if (links.length === 0) return null;
  const kind = (l: Link) => (l.field_key ? (fields.find((f) => f.key === l.field_key)?.label ?? l.field_key) : l.kind.replaceAll("_", " "));
  return (
    <Card title="Links" description="Companies, people, deals and other records this one is tied to.">
      <ul className="space-y-2 text-sm">
        {links.map((l) => (
          <li key={l.id} className="flex items-baseline justify-between gap-2">
            <button className="min-w-0 truncate text-left underline" onClick={() => onOpen(l.record.id)}>{l.record.name || "Unnamed"}</button>
            <span className="flex shrink-0 items-baseline gap-1.5 text-xs text-muted-foreground">
              {l.direction === "incoming" ? `${l.record.type} · ${kind(l)}` : kind(l)}
              {canManage && <button className="underline hover:text-foreground" aria-label={`Delete the link to ${l.record.name || "this record"}`} onClick={async () => (await deleteFromRecord(rec, { kind: "link", id: l.id, label: `the link to ${l.record.name || "this record"}` })) && onChange()}>Delete</button>}
            </span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function Consents({ brand, id, canManage, onChange, go }: { brand: string; id: string; canManage: boolean; onChange: () => void; go: (t: Tab) => void }) {
  const [list, setList] = useState<Consent[] | null>(null);
  const [history, setHistory] = useState(false);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/records/{record}/consents", { params: { path: { brand, record: id }, query: history ? { history: "true" } : {} } }).then(({ data }) => setList(data?.data ?? []));
  }, [brand, id, history]);
  if (!list) return null;
  return (
    <Card
      title="Consent"
      description={<>What they agreed to, or took back. Marketing email goes only to people whose email consent is granted. <button className="underline" onClick={() => go("topics")}>Topics</button></>}
      action={<button className="text-xs underline" onClick={() => setHistory(!history)}>{history ? "Current only" : "Every change"}</button>}
    >
      {list.length === 0 && <p className="text-sm text-muted-foreground">Never asked.</p>}
      <ul className="space-y-2 text-sm">
        {list.map((c) => (
          <li key={c.id} className="flex items-start justify-between gap-2">
            <div className="min-w-0">
              <div>{c.channel}{c.topic ? `: ${c.topic}` : ""} <span className="text-muted-foreground">({c.purpose})</span></div>
              <div className="text-xs text-muted-foreground" title={stamp(c.occurred_at)}>{c.source} · {ago(c.occurred_at)}</div>
            </div>
            <Badge tone={c.status === "granted" ? "green" : "red"}>{c.status === "granted" ? "Granted" : "Withdrawn"}</Badge>
          </li>
        ))}
      </ul>
      {canManage && <RecordConsent brand={brand} id={id} onAdded={onChange} />}
    </Card>
  );
}

const exportStatus: Record<PrivacyExport["status"], { label: string; tone: "green" | "amber" | "red" | "grey" }> = {
  pending: { label: "Building", tone: "amber" },
  ready: { label: "Ready", tone: "green" },
  failed: { label: "Failed", tone: "red" },
  expired: { label: "Expired", tone: "grey" },
};

const exportErrors: Record<string, string> = {
  too_large: "Over 100 MB: too much to export in one file.",
  record_gone: "The record was gone before it was built.",
  internal: "Outflare couldn't build it. Try again.",
};

function kilobytes(n: number | null | undefined): string {
  if (!n) return "";
  return n < 1024 * 1024 ? `${Math.max(1, Math.round(n / 1024))} KB` : `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// Privacy export: everything this record's brand holds about them, as a
// file, for a person asking what is kept about them. Exports of other
// brands' records never show here.
function PrivacyExports({ brand, record, canManage }: { brand: string; record: string; canManage: boolean }) {
  const [list, setList] = useState<PrivacyExport[] | null>(null);
  const [error, setError] = useState("");
  const [asking, setAsking] = useState(false);
  const [all, setAll] = useState(false);
  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/privacy-exports", { params: { path: { brand }, query: { record, limit: 20 } } }).then(({ data, error }) => {
      if (!data) return setError(errorMessage(error));
      setError("");
      setList(data.data);
    });
  }, [brand, record]);
  useEffect(load, [load]);
  // While one is being built, look again every few seconds.
  const building = list?.some((x) => x.status === "pending") ?? false;
  useEffect(() => {
    if (!building) return;
    const t = setTimeout(load, 2000);
    return () => clearTimeout(t);
  }, [building, list, load]);

  async function ask() {
    setAsking(true);
    const { error } = await api.POST("/v1/brands/{brand}/records/{record}/privacy-exports", { params: { path: { brand, record } } });
    setAsking(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Building the export. It's ready to download here in a moment.");
    load();
  }

  if (error) return <Card title="Privacy export"><ErrorText>{error}</ErrorText></Card>;
  if (!list) return null;
  if (list.length === 0 && !canManage) return null;
  const shown = all ? list : list.slice(0, 3);
  return (
    <Card
      title="Privacy export"
      description="Everything this brand holds about them, as one file, for a request to see their data. Only this brand's data, never secrets. Downloadable for 7 days."
      action={canManage && <Button variant="ghost" disabled={asking || building} onClick={ask}>{building ? "Building…" : "Export"}</Button>}
    >
      {list.length === 0 && <p className="text-sm text-muted-foreground">None yet.</p>}
      <ul className="space-y-2 text-sm">
        {shown.map((x) => {
          const rows = Object.values(x.counts).reduce((a, b) => a + b, 0);
          return (
            <li key={x.id} className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div title={stamp(x.created_at)}>Asked {ago(x.created_at)}</div>
                <div className="text-xs text-muted-foreground">
                  {x.status === "ready" && `${rows} rows · ${kilobytes(x.size_bytes)} · until ${x.expires_at ? when(x.expires_at) : ""}`}
                  {x.status === "failed" && (exportErrors[x.error_code ?? ""] ?? x.error_code)}
                  {x.status === "expired" && "The file was deleted after 7 days."}
                  {x.status === "pending" && "Gathering their data…"}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <Badge tone={exportStatus[x.status].tone}>{exportStatus[x.status].label}</Badge>
                {x.status === "ready" && canManage && (
                  <a className="text-xs underline" href={`/v1/brands/${brand}/privacy-exports/${x.id}/download`} download onClick={() => setTimeout(load, 1000)}>
                    Download
                  </a>
                )}
              </div>
            </li>
          );
        })}
      </ul>
      {list.length > 3 && <button className="mt-3 text-xs underline" onClick={() => setAll(!all)}>{all ? "Show fewer" : `Show all ${list.length}`}</button>}
    </Card>
  );
}

const kinds: Record<TimelineItem["kind"], string> = {
  event: "Product event",
  attribute_change: "Field changed",
  identity: "Email or id",
  consent: "Consent",
  message: "Email",
  meeting: "Meeting",
  deal: "Deal",
  visit: "Website visit",
  opt_out: "Do not contact",
  journey: "Journey",
  list: "List",
};

function Timeline({ brand, rec, cat, names, onOpen, canManage }: { brand: string; rec: Rec; cat: CatalogInfo; names: Record<string, string>; onOpen: (item: string) => void; canManage: boolean }) {
  const [items, setItems] = useState<TimelineItem[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [kind, setKind] = useState("");
  const [people, setPeople] = useState(true);
  const [error, setError] = useState("");
  const company = rec.type === "account";

  const load = useCallback(
    async (cursor?: string) => {
      const { data, error } = await api.GET("/v1/brands/{brand}/records/{record}/timeline", {
        params: { path: { brand, record: rec.id }, query: { limit: 50, ...(kind ? { kinds: kind } : {}), ...(company && !people ? { people: "false" as const } : {}), ...(cursor ? { cursor } : {}) } },
      });
      if (!data) return setError(errorMessage(error));
      setError("");
      setItems((prev) => (cursor ? [...(prev ?? []), ...data.data] : data.data));
      setNext(data.next_cursor ?? null);
    },
    [brand, rec.id, kind, people, company],
  );
  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Kind" className={field} value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">Everything</option>
          {Object.entries(kinds).map(([k, label]) => <option key={k} value={k}>{label}</option>)}
        </select>
        {company && (
          <label className="flex items-center gap-1.5 text-[13px]">
            <input type="checkbox" checked={people} onChange={(e) => setPeople(e.target.checked)} /> Include their people
          </label>
        )}
      </div>
      <ErrorText>{error}</ErrorText>
      {!items && !error && <Loading />}
      {items && items.length === 0 && <p className="text-sm text-muted-foreground">Nothing yet{kind ? " of this kind" : ""}.</p>}
      {items && items.length > 0 && (
        <ol className="divide-y rounded-md border">
          {items.map((it) => (
            <li key={`${it.kind}:${it.id}`} className="flex gap-3 px-3 py-2.5 text-sm">
              <span className="w-24 shrink-0 text-xs text-muted-foreground" title={stamp(it.at)}>{ago(it.at)}</span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium break-words">{itemTitle(it, typeOf(it, rec), cat)}</span>
                  <span className="text-xs text-muted-foreground">{kinds[it.kind] ?? it.kind}{it.source ? ` · ${it.source}` : ""}</span>
                </div>
                <div className="text-xs text-muted-foreground break-words">{itemDetail(it, typeOf(it, rec), cat, names)}</div>
                {it.record_id !== rec.id && (
                  <button className="text-xs underline" onClick={() => onOpen(it.record_id)}>{names[it.record_id] ?? "One of their people"}</button>
                )}
              </div>
              {canManage && it.kind === "event" && rec.brand_id && it.record_id === rec.id && (
                <button
                  className="shrink-0 self-start text-xs text-muted-foreground underline"
                  aria-label="Delete this event"
                  onClick={async () => {
                    if (await softDeleteEvent(rec.brand_id!, it.id, it.title)) setItems((prev) => prev?.filter((x) => !(x.kind === it.kind && x.id === it.id)) ?? null);
                  }}
                >
                  Delete
                </button>
              )}
            </li>
          ))}
        </ol>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
    </div>
  );
}

const text = (v: unknown) => (v === null || v === undefined || v === "" ? "" : typeof v === "object" ? JSON.stringify(v) : String(v));

// On a company's timeline, the items of its people are about a person.
const typeOf = (it: TimelineItem, rec: Rec) => (it.record_id === rec.id ? rec.type : "person");

function itemTitle(it: TimelineItem, type: string, cat: CatalogInfo): string {
  switch (it.kind) {
    case "attribute_change":
      return it.title === "name" ? "Name" : it.title === "archived" ? "Archived" : (cat.fields(type).find((f) => f.key === it.title)?.label ?? it.title);
    case "identity":
      return namespaces[it.title] ?? it.title;
    case "message":
      return it.title || "(no subject)";
  }
  return it.title || kinds[it.kind] || it.kind;
}

// One line on what happened, from the item's detail.
function itemDetail(it: TimelineItem, type: string, cat: CatalogInfo, names: Record<string, string>): string {
  const d = it.detail as Record<string, unknown>;
  switch (it.kind) {
    case "attribute_change": {
      const show = (v: unknown) => fieldLabelValue(cat, type, it.title, v, names) || "empty";
      return `${show(d.old_value)} → ${show(d.new_value)}${d.bulk_edit_id ? " · bulk edit" : ""}`;
    }
    case "identity":
      return [text(d.value), text(d.change), d.verified ? "verified" : ""].filter(Boolean).join(" · ");
    case "consent":
      return [text(d.status), text(d.topic), text(d.purpose)].filter(Boolean).join(" · ");
    case "message":
      return d.direction === "in" ? ["Reply", text(d.category)].filter(Boolean).join(" · ") : ["Sent", text(d.status), d.step ? `step ${d.step}` : ""].filter(Boolean).join(" · ");
    case "meeting":
      return [text(d.status), d.starts_at ? `starts ${stamp(String(d.starts_at))}` : "", d.qualified ? "qualified" : ""].filter(Boolean).join(" · ");
    case "deal": {
      const amount = typeof d.amount_cents === "number" && d.currency ? `${(d.amount_cents / 100).toLocaleString()} ${text(d.currency).toUpperCase()}` : "";
      return [text(d.stage), text(d.state), amount].filter(Boolean).join(" · ");
    }
    case "visit":
      return [d.pageviews ? `${d.pageviews} pages` : "", text(d.referrer_host) && `from ${text(d.referrer_host)}`, text(d.utm_campaign)].filter(Boolean).join(" · ");
    case "opt_out":
      return [text(d.kind), text(d.value), text(d.method)].filter(Boolean).join(" · ");
    case "journey":
      return [text(d.change), text(d.exit_reason)].filter(Boolean).join(" · ");
    case "list":
      return d.change === "enter" ? "joined" : "left";
    case "event": {
      const props = (d.properties ?? {}) as Record<string, unknown>;
      return Object.entries(props).filter(([, v]) => v !== null && typeof v !== "object").slice(0, 4).map(([k, v]) => `${k}: ${v}`).join(" · ");
    }
  }
  return "";
}

function History({ brand, rec, cat, names, onOpen }: { brand: string; rec: Rec; cat: CatalogInfo; names: Record<string, string>; onOpen: (item: string) => void }) {
  const [list, setList] = useState<Change[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const fields = cat.fields(rec.type);

  const load = useCallback(
    async (cursor?: string) => {
      const { data, error } = await api.GET("/v1/brands/{brand}/records/{record}/history", { params: { path: { brand, record: rec.id }, query: { limit: 50, ...(cursor ? { cursor } : {}) } } });
      if (!data) return setError(errorMessage(error));
      setList((prev) => (cursor ? [...(prev ?? []), ...data.data] : data.data));
      setNext(data.next_cursor ?? null);
    },
    [brand, rec.id],
  );
  useEffect(() => {
    load();
  }, [load]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  if (list.length === 0) return <p className="text-sm text-muted-foreground">No changes yet.</p>;
  return (
    <div className="space-y-3">
      <DataTable
        data={list}
        rowId={(c) => String(c.id)}
        columnsMenu={false}
        mobile={(c) => (
          <div className="text-sm">
            <div className="font-medium">{c.field === "name" ? "Name" : (fields.find((f) => f.key === c.field)?.label ?? c.field)}</div>
            <div className="text-xs text-muted-foreground">{fieldLabelValue(cat, rec.type, c.field, c.old_value, names) || "empty"} → {fieldLabelValue(cat, rec.type, c.field, c.new_value, names) || "empty"}</div>
            <div className="text-xs text-muted-foreground">{c.source}{c.bulk_edit_id ? " · bulk edit" : ""} · {ago(c.changed_at)}</div>
          </div>
        )}
        columns={[
          { id: "field", header: "Field", accessorFn: (c) => (c.field === "name" ? "Name" : c.field === "archived" ? "Archived" : (fields.find((f) => f.key === c.field)?.label ?? c.field)) },
          { id: "old", header: "Was", enableSorting: false, cell: ({ row: { original: c } }) => <FieldValue f={fields.find((f) => f.key === c.field)} v={c.old_value} cat={cat} names={names} onOpen={onOpen} /> },
          { id: "new", header: "Now", enableSorting: false, cell: ({ row: { original: c } }) => <FieldValue f={fields.find((f) => f.key === c.field)} v={c.new_value} cat={cat} names={names} onOpen={onOpen} /> },
          { id: "source", header: "Source", accessorFn: (c) => c.source, cell: ({ row: { original: c } }) => <span className="text-muted-foreground">{c.source}{c.bulk_edit_id ? " (bulk edit)" : c.actor_id ? " (a person)" : ""}</span> },
          { id: "when", header: "When", accessorFn: (c) => c.changed_at, cell: ({ row: { original: c } }) => <span className="text-muted-foreground" title={stamp(c.changed_at)}>{ago(c.changed_at)}</span> },
        ]}
      />
      {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
    </div>
  );
}
