import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Lead, LeadImportResult, LeadList } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, Empty, ErrorText, field, FilePicker, Loading, PageHeader, toast } from "../ui";
import { Search } from "lucide-react";
import { DataTable } from "@/components/data-table";
import { ago } from "../format";
import type { Tab } from "../App";
import { useBrand } from "@/lib/brand";

// How each verification result reads in the table. Unverified addresses are
// checked automatically before their first email, so they aren't a problem.
const verdicts: Record<string, { label: string; tone: "green" | "amber" | "red" | "grey"; title: string }> = {
  deliverable: { label: "Valid", tone: "green", title: "The mailbox exists and accepts mail." },
  risky: { label: "Risky", tone: "amber", title: "Catch-all, role or disposable address: it may bounce. Sent unless your verification settings skip risky addresses." },
  undeliverable: { label: "Invalid", tone: "red", title: "The address doesn't exist. It won't be emailed." },
  unknown: { label: "Unknown", tone: "grey", title: "The provider couldn't tell. Sent unless your verification settings skip unknown addresses." },
};

function Verdict({ v }: { v: Lead["verification"] }) {
  if (!v) return <span className="text-muted-foreground" title="Checked automatically before the first email.">Not yet</span>;
  const d = verdicts[v];
  return <Badge tone={d.tone} title={d.title}>{d.label}</Badge>;
}

export function Leads({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const [leads, setLeads] = useState<Lead[]>([]);
  const [total, setTotal] = useState(0);
  const [next, setNext] = useState<string | null>(null);
  const [lists, setLists] = useState<LeadList[]>([]);
  const [listId, setListId] = useState("");
  const [verification, setVerification] = useState("");
  const [q, setQ] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [importing, setImporting] = useState<boolean | null>(null);

  // Narrowed to the brand picked in the sidebar: leads on its lists.
  const { query: brandQuery } = useBrand(ws);
  const load = useCallback(
    async (after?: string) => {
      const query = { ...(listId ? { list_id: listId } : {}), ...(q ? { q } : {}), ...(verification ? { verification: verification as "deliverable" } : {}), ...(after ? { after } : {}), limit: 100, ...brandQuery };
      const [l, ls] = await Promise.all([
        api.GET("/v1/workspaces/{ws}/leads", { params: { path: { ws }, query } }),
        after ? Promise.resolve(null) : api.GET("/v1/workspaces/{ws}/lead-lists", { params: { path: { ws }, query: brandQuery } }),
      ]);
      if (l.data) {
        setLeads((prev) => (after ? [...prev, ...l.data.data] : l.data.data));
        setTotal(l.data.total);
        setNext(l.data.next_after ?? null);
      }
      if (ls?.data) setLists(ls.data.data);
      setLoaded(true);
    },
    [ws, listId, q, verification, brandQuery.brand], // eslint-disable-line react-hooks/exhaustive-deps
  );

  useEffect(() => {
    const t = setTimeout(() => load(), 200);
    return () => clearTimeout(t);
  }, [load]);

  async function remove(l: Lead) {
    const r = await ask({
      title: `Delete ${l.email}?`,
      body: (
        <>
          <p>The lead is removed from every list and sequence. Emails already sent stay in the message log.</p>
          <p>If they asked to have their data erased or never to be contacted, also add them to the suppression list, so a later import can't email them again.</p>
        </>
      ),
      choices: [
        { label: "Cancel", value: "cancel" },
        { label: "Delete", value: "delete", variant: "ghost" },
        { label: "Delete and never email", value: "suppress", variant: "danger-solid" },
      ],
    });
    if (!r) return;
    await api.DELETE("/v1/workspaces/{ws}/leads/{id}", { params: { path: { ws, id: l.id }, query: { suppress: r.choice === "suppress" } } });
    toast(r.choice === "suppress" ? `Deleted ${l.email} and added it to the suppression list.` : `Deleted ${l.email}.`);
    load();
  }

  const listName = (id: string) => lists.find((l) => l.id === id)?.name;
  const empty = loaded && total === 0 && !q && !listId && !verification;
  const showImport = canManage && (importing ?? empty);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Leads"
        description="The people your sequences email. Group them into lists, then add a list to a sequence."
        action={canManage && !empty && <Button variant={showImport ? "ghost" : "primary"} onClick={() => setImporting(!showImport)}>{showImport ? "Close" : "Import leads"}</Button>}
      />
      {showImport && <Import ws={ws} onDone={() => load()} go={go} />}
      {!loaded && <Loading />}
      {empty && !canManage && <Empty title="No leads yet" />}
      {loaded && !empty && (
        <div className="space-y-3">
          <DataTable
            data={leads}
            rowId={(l) => l.id}
            toolbar={
              <>
                <label className="relative min-w-0 flex-1 basis-60">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <input type="search" aria-label="Search leads" className={`${field} w-full pl-8`} placeholder="Search email, name or company" value={q} onChange={(e) => setQ(e.target.value)} />
                </label>
                <select aria-label="List" className={field} value={listId} onChange={(e) => setListId(e.target.value)}>
                  <option value="">All leads</option>
                  {lists.map((l) => <option key={l.id} value={l.id}>{l.name} ({l.leads})</option>)}
                </select>
                <select aria-label="Verification" className={field} value={verification} onChange={(e) => setVerification(e.target.value)}>
                  <option value="">Any verification</option>
                  <option value="deliverable">Valid</option>
                  <option value="risky">Risky</option>
                  <option value="undeliverable">Invalid</option>
                  <option value="unknown">Unknown</option>
                  <option value="unverified">Not checked yet</option>
                </select>
                <span className="font-mono text-[12px] text-muted-foreground">
                  {total.toLocaleString()} {total === 1 ? "lead" : "leads"}{listId ? ` in ${listName(listId) ?? "this list"}` : ""}
                </span>
              </>
            }
            empty={<>No leads match. <button className="underline" onClick={() => { setQ(""); setListId(""); setVerification(""); }}>Clear the search</button></>}
            mobile={(l) => (
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="break-all font-mono text-[13px]">{l.email}</div>
                  <div className="text-xs text-muted-foreground">{[[l.first_name, l.last_name].filter(Boolean).join(" "), l.title, l.company].filter(Boolean).join(" · ") || "No name"}</div>
                  <div className="mt-1 flex flex-wrap gap-1.5">
                    {l.verification && <Verdict v={l.verification} />}
                    {l.suppressed && <Badge tone="red" title="On the suppression list: never emailed">Won't be emailed</Badge>}
                  </div>
                </div>
                {canManage && <Button variant="danger" onClick={() => remove(l)}>Delete</Button>}
              </div>
            )}
            hidden={["created"]}
            columns={[
              {
                id: "email",
                header: "Email",
                accessorFn: (l) => l.email,
                cell: ({ row: { original: l } }) => (
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-[12px]">{l.email}</span>
                    {l.suppressed && <Badge tone="red" title="On the suppression list: never emailed">Won't be emailed</Badge>}
                  </span>
                ),
              },
              { id: "verification", header: "Verified", accessorFn: (l) => l.verification ?? "", cell: ({ row: { original: l } }) => <Verdict v={l.verification} /> },
              { id: "name", header: "Name", accessorFn: (l) => [l.first_name, l.last_name].filter(Boolean).join(" "), cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">–</span> },
              { id: "company", header: "Company", accessorFn: (l) => l.company ?? "", cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">–</span> },
              { id: "title", header: "Title", accessorFn: (l) => l.title ?? "", cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">–</span> },
              { id: "lists", header: "Lists", enableSorting: false, accessorFn: (l) => l.list_ids.map(listName).filter(Boolean).join(", "), cell: ({ getValue }) => <span className="text-muted-foreground">{getValue() as string}</span> },
              { id: "created", header: "Added", accessorFn: (l) => l.created_at, cell: ({ row: { original: l } }) => <span className="text-muted-foreground">{ago(l.created_at)}</span> },
              {
                id: "actions",
                header: () => <span className="sr-only">Actions</span>,
                enableSorting: false,
                enableHiding: false,
                meta: { align: "right" },
                cell: ({ row: { original: l } }) => canManage && <Button variant="danger" className="-my-1 opacity-0 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => remove(l)}>Delete</Button>,
              },
            ]}
          />
          {next && <Button variant="ghost" onClick={() => load(next)}>Show more ({(total - leads.length).toLocaleString()} left)</Button>}
        </div>
      )}
    </div>
  );
}

function Import({ ws, onDone, go }: { ws: string; onDone: () => void; go: (t: Tab) => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [list, setList] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<LeadImportResult | null>(null);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/leads/import", {
      params: { path: { ws }, query: list ? { list } : {} },
      body: await file.text(),
      bodySerializer: (b: string) => b,
      headers: { "Content-Type": "text/csv" },
    });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setError("");
    setResult(data ?? null);
    setFile(null);
    onDone();
  }

  return (
    <Card title="Import leads from a CSV" description="Exports from Apollo, Clay, your CRM or a spreadsheet all work.">
      <form onSubmit={submit} className="space-y-4 text-sm">
        <p className="text-muted-foreground">
          Only an <code>email</code> column is required. Name, company, title, phone, LinkedIn and website columns are recognised by their usual names. Every other column is kept as a custom field, so a <code>pain_point</code> column becomes <code>{"{{pain_point}}"}</code> in your emails. Leads already here are updated, not duplicated.
        </p>
        <FilePicker file={file} onChange={setFile} accept=".csv,text/csv" example={{ name: "leads-example.csv", content: "email,first_name,last_name,company,title,pain_point\nana@acme-com.example,Ana,Diaz,Acme,Founder,slow outbound\n" }} />
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="mb-1 block font-medium">Add them to a list <span className="font-normal text-muted-foreground">(optional)</span></span>
            <input className={field} placeholder="e.g. Q4 founders" value={list} onChange={(e) => setList(e.target.value)} />
            <span className="mt-1 block text-xs text-muted-foreground">A new name creates the list. You add lists to sequences.</span>
          </label>
          <Button disabled={!file || busy} title={!file ? "Choose a CSV file first" : undefined}>{busy ? "Importing…" : "Import leads"}</Button>
        </div>
        <ErrorText>{error}</ErrorText>
        {result && (
          <div className="rounded bg-muted p-3">
            <p>
              {result.created.toLocaleString()} added, {result.updated.toLocaleString()} updated
              {result.duplicates > 0 && `, ${result.duplicates} duplicate rows merged`}
              {result.errors.length > 0 && `, ${result.errors.length} skipped`}.
              {result.suppressed > 0 && ` ${result.suppressed} are on the suppression list and won't be emailed.`}
            </p>
            {result.list_id && <button type="button" className="mt-2 underline" onClick={() => go("sequences")}>Add this list to a sequence →</button>}
            {result.errors.length > 0 && (
              <ul className="mt-2 max-h-40 space-y-1 overflow-y-auto text-red-600 dark:text-red-400">
                {result.errors.slice(0, 200).map((e) => <li key={`${e.line}-${e.email}`}>Line {e.line}{e.email ? ` (${e.email})` : ""}: {e.error}</li>)}
              </ul>
            )}
          </div>
        )}
      </form>
    </Card>
  );
}
