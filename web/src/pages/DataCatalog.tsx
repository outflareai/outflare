import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, CopyButton, ErrorText, Loading, PageHeader } from "../ui";
import { ago } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { PickBrand } from "@/components/pick-brand";
import { DataTable } from "@/components/data-table";
import type { Tab } from "../App";

// The data catalog (AGENTS.md, "Data: agents can see all of it"), in the
// Data menu: every table of the brand's data that Query and agents read,
// what it holds, where it comes from and how fresh it is, read live. Each
// table opens on a page of its own (/data-catalog/<table>) with every
// column explained. Agents read the same thing with describe_brand_data.

type Table = Schemas["DataCatalogTable"];
type Column = Schemas["DataCatalogColumn"];

const areaNames: Record<string, string> = {
  customers: "Customers",
  revenue: "Revenue",
  ads: "Ads",
  analytics: "Analytics",
  outreach: "Outreach",
  email: "Email",
  seo: "SEO",
  coverage: "Coverage",
  social: "Social",
  content: "Content",
  agents: "Agents",
  workspace: "Workspace",
};

const count = (n: number | null | undefined) => (n == null ? "–" : n.toLocaleString());
const day = (s: string | null | undefined) => (s ? s.slice(0, 10) : "–");

export function DataCatalog({ ws, open, onOpen, go }: { ws: string; open: string; onOpen: (item: string) => void; go: (t: Tab, item?: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("data-catalog", "Catalog"), []);
  if (!brand) {
    return (
      <div className="space-y-6">
        <PageHeader title="Data catalog" />
        <PickBrand what="Data catalog" brands={active.length} />
      </div>
    );
  }
  if (open) return <TablePage key={brand.id + open} brand={brand.id} name={open} onBack={() => onOpen("")} go={go} />;
  return <TableList key={brand.id} brand={brand.id} onOpen={onOpen} />;
}

function TableList({ brand, onOpen }: { brand: string; onOpen: (item: string) => void }) {
  const [list, setList] = useState<Table[] | null>(null);
  const [at, setAt] = useState("");
  const [note, setNote] = useState<string | null>(null);
  const [area, setArea] = useState("");
  const [error, setError] = useState("");
  const [counting, setCounting] = useState(false);
  // The list shows at once from what is written about each table; the
  // numbers are counted live and can take seconds, so they fill in after.
  const load = async (fresh = false) => {
    setError("");
    setCounting(true);
    if (!fresh) {
      const first = await api.GET("/v1/brands/{brand}/data/catalog", { params: { path: { brand }, query: { numbers: false } } });
      if (first.error) {
        setCounting(false);
        return setError(errorMessage(first.error));
      }
      setList((cur) => cur ?? first.data.data);
    }
    const { data, error } = await api.GET("/v1/brands/{brand}/data/catalog", { params: { path: { brand }, query: fresh ? { fresh: true } : {} } });
    setCounting(false);
    if (error) return setNote("The numbers couldn't be read just now. The tables are listed without them; try Refresh numbers.");
    setList(data.data);
    setAt(data.computed_at);
    setNote(data.note);
  };
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand]);
  if (error) return <div className="space-y-4"><PageHeader title="Data catalog" /><ErrorText>{error}</ErrorText></div>;
  if (!list) return <Loading />;
  const areas = [...new Set(list.map((t) => t.area))];
  const shown = area ? list.filter((t) => t.area === area) : list;
  return (
    <div className="space-y-4">
      <PageHeader
        title="Data catalog"
        description="Every table of this brand's data that you, Queries and connected agents can read with SQL: what it holds, where it comes from and how fresh it is. Agents read the same catalog with describe_brand_data."
        action={<Button variant="ghost" disabled={counting} onClick={() => load(true)}>{counting ? "Reading numbers…" : "Refresh numbers"}</Button>}
      />
      {note && <p className="text-[13px] text-amber-700 dark:text-amber-400">{note}</p>}
      <DataTable<Table>
        data={shown}
        rowId={(t) => t.name}
        onRowClick={(t) => onOpen(t.name)}
        searchPlaceholder="Search tables"
        view="data-catalog"
        toolbar={
          <select aria-label="Area" className="h-8 rounded-md border bg-background px-2 text-[13px]" value={area} onChange={(e) => setArea(e.target.value)}>
            <option value="">All areas</option>
            {areas.map((a) => (
              <option key={a} value={a}>{areaNames[a] ?? a}</option>
            ))}
          </select>
        }
        columns={[
          { id: "name", header: "Table", accessorFn: (t) => t.name, cell: ({ row: { original: t } }) => <span className="font-mono text-[13px]">{t.name}</span> },
          { id: "area", header: "Area", accessorFn: (t) => areaNames[t.area] ?? t.area },
          { id: "description", header: "What a row is", accessorFn: (t) => t.description, cell: ({ getValue }) => <span className="line-clamp-2 text-muted-foreground">{getValue() as string}</span> },
          { id: "rows", header: "Rows", accessorFn: (t) => t.rows ?? -1, meta: { align: "right" }, cell: ({ row: { original: t } }) => <span className="font-mono tabular-nums">{t.rows == null && counting && t.available ? <span className="text-muted-foreground">…</span> : count(t.rows)}</span> },
          { id: "last", header: "Latest", accessorFn: (t) => t.last_at ?? "", cell: ({ row: { original: t } }) => <span className="font-mono text-[12px]">{day(t.last_at)}</span> },
          {
            id: "available",
            header: "You can read",
            accessorFn: (t) => (t.available ? 1 : 0),
            cell: ({ row: { original: t } }) => (t.available ? <Badge tone="green">Yes</Badge> : <Badge tone="grey" title={"Needs " + t.scope}>No</Badge>),
          },
        ]}
      />
      <p className="text-[12px] text-muted-foreground">{counting ? "Reading rows and dates…" : at ? `Numbers read ${ago(at)}.` : ""}</p>
    </div>
  );
}

function TablePage({ brand, name, onBack, go }: { brand: string; name: string; onBack: () => void; go: (t: Tab, item?: string) => void }) {
  const [table, setTable] = useState<Table | null>(null);
  const [cols, setCols] = useState<Column[]>([]);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [counting, setCounting] = useState(true);
  useEffect(() => {
    let stale = false;
    const read = (numbers: boolean) => api.GET("/v1/brands/{brand}/data/catalog/{table}", { params: { path: { brand, table: name }, query: numbers ? {} : { numbers: false } } });
    // The columns show at once; rows, filled shares and values follow.
    read(false).then(async (first) => {
      if (stale) return;
      if (first.error) {
        setCounting(false);
        return setError(errorMessage(first.error));
      }
      setTable(first.data.table);
      setCols(first.data.columns);
      setNote(first.data.note);
      if (!first.data.table.available || first.data.table.rows != null) return setCounting(false);
      const { data } = await read(true);
      if (stale) return;
      setCounting(false);
      if (!data) return setNote("This table's numbers couldn't be read just now; its columns are described without them.");
      setTable(data.table);
      setCols(data.columns);
      setNote(data.note);
    });
    return () => {
      stale = true;
    };
  }, [brand, name]);
  if (error) return <div className="space-y-4"><PageHeader title={name} back={{ label: "Data catalog", onClick: onBack }} /><ErrorText>{error}</ErrorText></div>;
  if (!table) return <Loading />;
  return (
    <div className="space-y-6">
      <PageHeader
        title={<span className="font-mono">{table.name}</span>}
        description={table.description}
        back={{ label: "Data catalog", onClick: onBack }}
        action={<Button variant="ghost" onClick={() => go("query")}>Open Queries</Button>}
      />
      <dl className="grid gap-3 text-[13px] sm:grid-cols-4">
        <div><dt className="text-muted-foreground">Area</dt><dd>{areaNames[table.area] ?? table.area}</dd></div>
        <div><dt className="text-muted-foreground">Rows</dt><dd className="font-mono tabular-nums">{table.rows == null && counting ? "Reading…" : count(table.rows)}</dd></div>
        <div><dt className="text-muted-foreground">From</dt><dd className="font-mono">{day(table.first_at)} to {day(table.last_at)}</dd></div>
        <div><dt className="text-muted-foreground">Read scope</dt><dd className="font-mono">{table.scope}</dd></div>
        <div className="sm:col-span-4"><dt className="text-muted-foreground">Where the rows come from</dt><dd>{table.source}</dd></div>
      </dl>
      {note && <p className="text-[13px] text-amber-700 dark:text-amber-400">{note}</p>}
      <DataTable<Column>
        data={cols}
        rowId={(c) => c.name}
        searchPlaceholder="Search columns"
        view="data-catalog-columns"
        columns={[
          { id: "name", header: "Column", accessorFn: (c) => c.name, cell: ({ row: { original: c } }) => <span className="font-mono text-[13px]">{c.name}{c.personal && <span className="ml-1.5"><Badge tone="amber" title="Holds personal data">Personal</Badge></span>}</span> },
          { id: "description", header: "Meaning", accessorFn: (c) => c.description },
          { id: "type", header: "Type and unit", accessorFn: (c) => c.unit ?? c.type, cell: ({ row: { original: c } }) => <span className="text-[12px]"><span className="font-mono">{c.type}</span>{c.unit && <span className="block text-muted-foreground">{c.unit}</span>}</span> },
          { id: "joins", header: "Joins to", accessorFn: (c) => c.joins_to ?? "", cell: ({ getValue }) => <span className="font-mono text-[12px]">{(getValue() as string) || "–"}</span> },
          { id: "filled", header: "Filled", accessorFn: (c) => c.filled ?? -1, meta: { align: "right" }, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{c.filled == null ? "–" : Math.round(c.filled * 100) + "%"}</span> },
          {
            id: "values",
            header: "Common values",
            accessorFn: (c) => (c.values ?? []).map((v) => String(v.value)).join(", "),
            cell: ({ row: { original: c } }) => <span className="text-[12px] text-muted-foreground">{(c.values ?? []).map((v) => `${String(v.value)} (${v.rows.toLocaleString()})`).join(", ") || "–"}</span>,
          },
        ]}
      />
      {table.examples.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold">Example questions</h2>
          {table.examples.map((e) => (
            <div key={e.question} className="rounded-md border p-3">
              <div className="flex items-start justify-between gap-3">
                <p className="text-[13px]">{e.question}</p>
                <CopyButton text={e.sql} label="Copy SQL" />
              </div>
              <pre className="mt-2 whitespace-pre-wrap font-mono text-[12px] text-muted-foreground">{e.sql}</pre>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}
