import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Suppression as Entry, SuppressionReason } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { ago } from "../format";
import { Search } from "lucide-react";
import { DataTable } from "@/components/data-table";

const reasons: SuppressionReason[] = ["do_not_contact", "manual", "unsubscribed", "bounced", "complained", "competitor", "customer", "partner", "legal"];
const reasonLabel: Record<SuppressionReason, string> = { do_not_contact: "Do not contact", manual: "Added by hand", unsubscribed: "Unsubscribed", bounced: "Bounced", complained: "Marked as spam",
  competitor: "Competitor", customer: "Customer (no cold email)", partner: "Partner (no cold email)", legal: "Legal request" };

export function Suppression({ ws, canAdd, canRemove }: { ws: string; canAdd: boolean; canRemove: boolean }) {
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [total, setTotal] = useState(0);
  const [next, setNext] = useState<string | null>(null);
  const [q, setQ] = useState("");

  const load = useCallback(
    async (after?: string) => {
      const { data } = await api.GET("/v1/workspaces/{ws}/suppressions", { params: { path: { ws }, query: { ...(q ? { q } : {}), ...(after ? { after } : {}) } } });
      if (!data) return;
      setEntries((prev) => (after ? [...(prev ?? []), ...data.data] : data.data));
      setTotal(data.total);
      setNext(data.next_after ?? null);
    },
    [ws, q],
  );

  useEffect(() => {
    const t = setTimeout(() => load(), 200);
    return () => clearTimeout(t);
  }, [load]);

  async function remove(e: Entry) {
    const shown = e.kind === "domain" ? `everyone at ${e.value}` : e.value;
    if (!(await confirmAction(`Remove ${e.kind === "domain" ? e.value : e.value} from the suppression list?`, <p>Sequences could email {shown} again. {e.reason === "unsubscribed" || e.reason === "complained" ? <b>They asked not to be emailed: removing them may break anti-spam law.</b> : null}</p>, "Remove"))) return;
    await api.DELETE("/v1/workspaces/{ws}/suppressions/{id}", { params: { path: { ws, id: e.id } } });
    toast(`Removed ${e.value}. It can be emailed again.`);
    load();
  }

  return (
    <div className="space-y-6">
      <PageHeader title="Do not contact" description="People and companies that are never emailed. Every email is checked against this list right before it's sent. Unsubscribes, bounces and removal requests are added automatically." />
      {canAdd && <Add ws={ws} onDone={() => load()} />}
      {entries === null ? (
        <Loading />
      ) : (
        <div className="space-y-3">
          <DataTable
            data={entries}
            rowId={(e) => e.id}
            empty={q ? "Nothing matches." : "Nothing here yet. Unsubscribes, bounces and spam complaints land here on their own."}
            toolbar={
              <>
                <label className="relative min-w-0 flex-1 basis-56">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <input type="search" aria-label="Search the suppression list" className={`${field} w-full pl-8`} placeholder="Search emails and domains" value={q} onChange={(e) => setQ(e.target.value)} />
                </label>
                <span className="font-mono text-[12px] text-muted-foreground tabular-nums">{total.toLocaleString()} {total === 1 ? "entry" : "entries"}</span>
              </>
            }
            mobile={(e) => (
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="font-mono text-[12px] break-all">{e.value} {e.kind === "domain" && <Badge title={`Blocks every address @${e.value}`}>Whole domain</Badge>}</div>
                  <div className="text-xs text-muted-foreground">{reasonLabel[e.reason] ?? e.reason} · {ago(e.created_at)}{e.note && ` · ${e.note}`}</div>
                </div>
                {canRemove && <Button variant="danger" onClick={() => remove(e)}>Remove</Button>}
              </div>
            )}
            columns={[
              {
                id: "value",
                header: "Email or domain",
                accessorFn: (e) => e.value,
                cell: ({ row: { original: e } }) => (
                  <>
                    <span className="font-mono text-[12px]">{e.value}</span> {e.kind === "domain" && <Badge title={`Blocks every address @${e.value}`}>Whole domain</Badge>}
                    {e.note && <div className="text-xs text-muted-foreground">{e.note}</div>}
                  </>
                ),
              },
              { id: "reason", header: "Why", accessorFn: (e) => reasonLabel[e.reason] ?? e.reason },
              { id: "created", header: "Added", accessorFn: (e) => e.created_at, cell: ({ row: { original: e } }) => <span className="text-muted-foreground" title={new Date(e.created_at).toLocaleString()}>{ago(e.created_at)}</span> },
              {
                id: "actions",
                header: () => <span className="sr-only">Actions</span>,
                enableSorting: false,
                enableHiding: false,
                meta: { align: "right" },
                cell: ({ row: { original: e } }) => canRemove && <Button variant="danger" className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => remove(e)}>Remove</Button>,
              },
            ]}
          />
          {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
        </div>
      )}
    </div>
  );
}

function Add({ ws, onDone }: { ws: string; onDone: () => void }) {
  const [text, setText] = useState("");
  const [reason, setReason] = useState<SuppressionReason>("manual");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");

  async function loadFile(f: File | undefined) {
    if (!f) return;
    // One value per line; for a CSV, the first column. A header row is
    // rejected as invalid and reported, which is harmless.
    const lines = (await f.text()).split(/\r?\n/).map((l) => l.split(",")[0].replace(/^"|"$/g, "").trim()).filter(Boolean);
    setText(lines.join("\n"));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const values = text.split(/[\s,;]+/).filter(Boolean);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/suppressions", { params: { path: { ws } }, body: { values, reason, ...(note ? { note } : {}) } });
    if (error) return setError(errorMessage(error));
    setText("");
    setNote("");
    setError(data.invalid.length ? `Not added, not an email or domain: ${data.invalid.slice(0, 5).map((i) => i.value).join(", ")}${data.invalid.length > 5 ? ` and ${data.invalid.length - 5} more` : ""}` : "");
    toast(`${data.added} added${data.existing ? `, ${data.existing} were already on the list` : ""}.`);
    onDone();
  }

  return (
    <Card title="Add to the list" description="Customers, competitors, partners, or anyone who asked not to be contacted.">
      <form onSubmit={submit} className="space-y-3 text-sm">
        <label className="block">
          <span className="mb-1 block font-medium">Emails or domains, one per line</span>
          <textarea className={`${field} min-h-24 w-full font-mono`} placeholder={"bob@acme-com.example\ncompetitor.com"} value={text} onChange={(e) => setText(e.target.value)} />
          <span className="mt-1 block text-xs text-muted-foreground">A domain blocks everyone at it. Or <label className="cursor-pointer underline">load a CSV or text file<input type="file" className="sr-only" accept=".csv,.txt,text/csv,text/plain" onChange={(e) => loadFile(e.target.files?.[0])} /></label> (first column).</span>
        </label>
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="mb-1 block font-medium">Why</span>
            <select className={field} value={reason} onChange={(e) => setReason(e.target.value as SuppressionReason)}>
              {reasons.map((r) => <option key={r} value={r}>{reasonLabel[r]}</option>)}
            </select>
          </label>
          <label className="block flex-1 basis-48">
            <span className="mb-1 block font-medium">Note <span className="font-normal text-muted-foreground">(optional)</span></span>
            <input className={`${field} w-full`} placeholder="e.g. Existing customer" maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} />
          </label>
          <Button disabled={!text.trim()}>Add to list</Button>
        </div>
        <ErrorText>{error}</ErrorText>
      </form>
    </Card>
  );
}
