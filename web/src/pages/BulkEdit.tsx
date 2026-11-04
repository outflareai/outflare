import { useEffect, useState, type FormEvent } from "react";
import type { BulkEdit, BulkEditInput, SmartList } from "@outflare/sdk";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, toast } from "../ui";
import { plural } from "../format";

// Bulk edits (docs/bulk-edits.md): change everyone in a list, the records
// picked on People or Companies, or a saved query's records at once.
// Anyone who may change records previews; only owners run one, which the
// preview's can_run says. Every change lands in each record's history.

type Changes = BulkEditInput["changes"];
type Preview = { record_type: string; records: number; can_run: boolean; sample: { id: string; name: string; changes: { field: string; from: unknown; to: unknown }[] }[] };
type Member = { user_id: string; name: string; email: string };

const tags = (s: string) => s.split(",").map((t) => t.trim()).filter(Boolean);

function show(v: unknown): string {
  if (v === null || v === undefined || v === "") return "empty";
  if (Array.isArray(v)) return v.length ? v.join(", ") : "none";
  return typeof v === "object" ? JSON.stringify(v) : String(v);
}

// BulkSource is which records a bulk edit changes: a list's members, ids
// picked by hand, or a saved query's record_id (or id) column.
export type BulkSource =
  | { kind: "list"; list: SmartList }
  | { kind: "records"; ids: string[]; type: "person" | "account" }
  | { kind: "query"; id: string; name: string };

export function BulkEditSheet({ ws, brand, source, onClose, onDone }: { ws: string; brand: string; source: BulkSource; onClose: () => void; onDone: () => void }) {
  const list = source.kind === "list" ? source.list : null;
  const [members, setMembers] = useState<Member[]>([]);
  const [statics, setStatics] = useState<SmartList[]>([]);
  const [owner, setOwner] = useState("");
  const [addTags, setAddTags] = useState("");
  const [removeTags, setRemoveTags] = useState("");
  const [addTo, setAddTo] = useState("");
  const [removeFrom, setRemoveFrom] = useState("");
  const [archive, setArchive] = useState(false);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [run, setRun] = useState<BulkEdit | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // A saved query's records are known once the preview has run it.
  const people = list ? list.record_type !== "account" : source.kind === "records" ? source.type !== "account" : (preview?.record_type ?? "person") !== "account";
  const title = source.kind === "list" ? source.list.name : source.kind === "records" ? plural(source.ids.length, source.type === "account" ? "company" : "person", source.type === "account" ? "companies" : "people") : source.name;
  const what = list ? "everyone in this list" : source.kind === "records" ? "these records" : "the records this query returns (its record_id or id column, up to 10,000)";

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setMembers((data?.data ?? []) as Member[]));
    api.GET("/v1/brands/{brand}/smart-lists", { params: { path: { brand } } }).then(({ data }) =>
      setStatics((data?.data ?? []).filter((l) => l.kind === "static" && l.id !== list?.id)),
    );
  }, [ws, brand, list?.id]);

  const changes: Changes = {
    ...(owner ? { set: { owner: owner === "none" ? null : owner } } : {}),
    ...(tags(addTags).length ? { add_tags: tags(addTags) } : {}),
    ...(tags(removeTags).length ? { remove_tags: tags(removeTags) } : {}),
    ...(addTo ? { add_to_list: addTo } : {}),
    ...(removeFrom ? { remove_from_list: removeFrom } : {}),
    ...(archive ? { archive: true } : {}),
  };
  const empty = Object.keys(changes).length === 0;
  const body = source.kind === "list" ? { smart_list_id: source.list.id, changes } : source.kind === "records" ? { record_ids: source.ids, changes } : { saved_query_id: source.id, changes };

  // A changed form needs a new preview.
  useEffect(() => setPreview(null), [owner, addTags, removeTags, addTo, removeFrom, archive]);

  // Follow a running bulk edit until it finishes.
  useEffect(() => {
    if (!run || run.status === "done" || run.status === "failed") return;
    const t = setInterval(async () => {
      const { data } = await api.GET("/v1/brands/{brand}/bulk-edits/{bulk_edit}", { params: { path: { brand, bulk_edit: run.id } } });
      if (data) setRun(data);
    }, 1500);
    return () => clearInterval(t);
  }, [run, brand]);
  useEffect(() => {
    if (run?.status === "done") {
      toast(`Changed ${plural(run.counts.changed ?? 0, people ? "person" : "company", people ? "people" : "companies")}.`);
      onDone();
    }
  }, [run?.status]); // eslint-disable-line react-hooks/exhaustive-deps

  async function check(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/bulk-edits/preview", { params: { path: { brand } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    setPreview(data as Preview);
  }

  async function go() {
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/brands/{brand}/bulk-edits", { params: { path: { brand } }, body });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "id" in data) setRun(data);
  }

  async function undo() {
    if (!run) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/bulk-edits/{bulk_edit}/undo", { params: { path: { brand, bulk_edit: run.id } } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    if (data && "id" in data) {
      toast("Undoing: the records go back as they were.");
      setRun(data);
    }
  }

  // One change in words: members by name, lists by name.
  const who = (v: unknown) => (typeof v === "string" ? (members.find((m) => m.user_id === v)?.name || members.find((m) => m.user_id === v)?.email) : undefined) ?? show(v);
  function describe(c: Preview["sample"][number]["changes"][number]) {
    if (c.field.startsWith("list:")) {
      const name = statics.find((l) => l.id === c.field.slice(5))?.name ?? "a list";
      return c.to ? `joins ${name}` : `leaves ${name}`;
    }
    if (c.field === "archived") return "archived";
    if (c.field === "owner") return `owner ${who(c.from)} → ${who(c.to)}`;
    return `${c.field} ${show(c.from)} → ${show(c.to)}`;
  }

  const running = run && (run.status === "queued" || run.status === "running");
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Bulk edit {title}</SheetTitle>
          <SheetDescription>
            Change {what} at once. Each change is kept in each {people ? "person's" : "company's"} history, and you can undo it for 7 days. Only owners can run a bulk edit.
          </SheetDescription>
        </SheetHeader>
        <form onSubmit={check} className="space-y-5 px-4 pb-6 text-[13px]">
          <label className="block">
            <span className="mb-1 block font-medium">Owner</span>
            <select className={`${field} w-full`} value={owner} onChange={(e) => setOwner(e.target.value)}>
              <option value="">Leave as it is</option>
              <option value="none">No owner</option>
              {members.map((m) => <option key={m.user_id} value={m.user_id}>{m.name || m.email}</option>)}
            </select>
          </label>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1 block font-medium">Add tags</span>
              <input className={`${field} w-full`} placeholder="vip, churn-risk" value={addTags} onChange={(e) => setAddTags(e.target.value)} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Remove tags</span>
              <input className={`${field} w-full`} placeholder="trial" value={removeTags} onChange={(e) => setRemoveTags(e.target.value)} />
            </label>
          </div>
          {people && statics.length > 0 && (
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="block">
                <span className="mb-1 block font-medium">Add to a static list</span>
                <select className={`${field} w-full`} value={addTo} onChange={(e) => setAddTo(e.target.value)}>
                  <option value="">None</option>
                  {statics.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
                </select>
              </label>
              <label className="block">
                <span className="mb-1 block font-medium">Take out of a static list</span>
                <select className={`${field} w-full`} value={removeFrom} onChange={(e) => setRemoveFrom(e.target.value)}>
                  <option value="">None</option>
                  {statics.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
                </select>
              </label>
            </div>
          )}
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={archive} onChange={(e) => setArchive(e.target.checked)} />
            Archive them
          </label>

          {preview && (
            <div aria-live="polite" className="space-y-2 rounded-md border bg-muted/40 p-3">
              <p>
                <span className="font-mono text-base font-medium tabular-nums">{preview.records.toLocaleString()}</span>{" "}
                {people ? (preview.records === 1 ? "person" : "people") : preview.records === 1 ? "company" : "companies"} will be checked. For example:
              </p>
              <ul className="space-y-1 text-[12px] text-muted-foreground">
                {preview.sample.slice(0, 8).map((r) => (
                  <li key={r.id}>
                    <span className="font-medium text-foreground">{r.name || "Unnamed"}</span>:{" "}
                    {r.changes.length === 0 ? "already so" : r.changes.map((c) => describe(c)).join("; ")}
                  </li>
                ))}
              </ul>
              {!preview.can_run && <p className="text-[12px]">Only the workspace's owners can run a bulk edit: ask one to run it.</p>}
            </div>
          )}

          {run && (
            <div aria-live="polite" className="rounded-md border p-3">
              {running && <p>Changing them now… {run.counts.done ?? 0} of {run.records} done.</p>}
              {run.status === "done" && (
                <p>
                  Done: {run.counts.changed ?? 0} changed, {run.counts.unchanged ?? 0} already so{run.counts.skipped ? `, ${run.counts.skipped} skipped (deleted or merged)` : ""}
                  {run.counts.kept ? `, ${run.counts.kept} kept as someone changed them since` : ""}.
                </p>
              )}
              {run.status === "failed" && <ErrorText>{run.error ?? "The bulk edit failed."}</ErrorText>}
            </div>
          )}

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            {!run && <Button variant={preview ? "ghost" : "primary"} disabled={busy || empty}>{busy && !preview ? "Checking…" : "Preview"}</Button>}
            {!run && preview?.can_run && <Button type="button" onClick={go} disabled={busy}>{busy ? "Starting…" : `Change ${preview.records.toLocaleString()}`}</Button>}
            {run?.undo_until && run.source !== "undo" && <Button type="button" variant="ghost" onClick={undo} disabled={busy}>Undo</Button>}
            <Button type="button" variant="ghost" onClick={onClose}>{run ? "Close" : "Cancel"}</Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}
