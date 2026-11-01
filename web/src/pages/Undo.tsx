import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";

// Undo for bad data: a merge can be undone (every row it moved goes back),
// and an import or a send of events can be rolled back in one step, after
// a preview of what it made and what changed since. Everything a batch
// undo removes goes to Recently deleted, restorable for 7 days.

type Merge = Schemas["Merge"];
type Batch = Schemas["Batch"];
type Preview = Schemas["BatchUndoPreview"];
type Changed = Schemas["BatchChangedItem"];

// ---- Merges on a record page ----

export function RecordMerges({ brand, record, canManage, onOpen, onChange }: { brand: string; record: string; canManage: boolean; onOpen: (item: string) => void; onChange: () => void }) {
  const [list, setList] = useState<Merge[]>([]);
  const [busy, setBusy] = useState("");

  const load = useCallback(() => {
    api.GET("/v1/brands/{brand}/merges", { params: { path: { brand }, query: { record, limit: 20 } } }).then(({ data }) => data && setList(data.data));
  }, [brand, record]);
  useEffect(load, [load]);

  async function undo(m: Merge) {
    const ok = await confirmAction(
      "Undo this merge?",
      <>
        <p>The merged record becomes a record of its own again, and everything the merge moved goes back to it: its emails and ids, links, events, consents and what was sent to it.</p>
        <p>What happened on the kept record since the merge stays there. Nothing is sent and no rule or journey fires.</p>
      </>,
      "Undo merge",
      false,
    );
    if (!ok) return;
    setBusy(m.id);
    const { data, error } = await api.POST("/v1/brands/{brand}/merges/{merge}/undo", { params: { path: { brand, merge: m.id } } });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    if (data && "not_restored" in data && data.not_restored.length > 0) {
      toast(`Merge undone. ${plural(data.not_restored.length, "value")} changed since the merge kept ${data.not_restored.length === 1 ? "its" : "their"} new value.`);
    } else toast(data && "merge" in data ? "Merge undone." : "Held for approval in the mission.");
    load();
    onChange();
  }

  if (list.length === 0) return null;
  return (
    <Card title="Merges" description="Records merged into this one, or this one into another.">
      <ul className="space-y-2 text-sm">
        {list.map((m) => {
          const other = m.into_id === record ? m.from_id : m.into_id;
          return (
            <li key={m.id} className="flex flex-wrap items-start gap-2">
              <div className="min-w-0 flex-1">
                {m.into_id === record ? "Another record was merged into this one" : "This record was merged into another"}{" "}
                <span className="text-muted-foreground" title={stamp(m.merged_at)}>{ago(m.merged_at)}</span>.{" "}
                <button className="underline" onClick={() => onOpen(other)}>Open the other record</button>
                {m.undone_at && <Badge>Undone</Badge>}
                {!m.reversible && !m.undone_at && <div className="text-xs text-muted-foreground">Made before merges could be undone.</div>}
                {m.reversible && !m.undone_at && !m.undoable && <div className="text-xs text-muted-foreground">A later merge involves one of these records: undo that one first.</div>}
              </div>
              {canManage && m.undoable && (
                <Button variant="ghost" disabled={busy === m.id} onClick={() => undo(m)}>{busy === m.id ? "Undoing…" : "Undo merge"}</Button>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

// ---- Batches ----

const batchKinds: Record<Batch["kind"], string> = {
  csv_import: "CSV import",
  crm_import: "CRM import",
  events: "Events sent",
};

const madeWords: Record<string, [string, string]> = {
  records: ["person or company", "people and companies"],
  field_values: ["field value", "field values"],
  identities: ["email or id", "emails and ids"],
  relationships: ["link", "links"],
  events: ["product event", "product events"],
  list_memberships: ["list place", "list places"],
  leads: ["lead", "leads"],
};

const changedWords: Record<Changed["kind"], string> = {
  edited: "edited by someone since",
  emailed: "sent an email since",
  paid: "has payments since",
  activity: "has other activity since",
  merged: "merged since (undo the merge first; never undone here)",
  field_changed: "a field it set was changed since (keeps its new value)",
  lead_in_sequence: "lead already in a sequence (stays)",
};

function made(counts: Record<string, number>): string {
  const parts = Object.entries(madeWords)
    .filter(([k]) => (counts[k] ?? 0) > 0)
    .map(([k, [one, many]]) => plural(counts[k], one, many));
  return parts.length ? parts.join(", ") : "nothing that is still here";
}

export function Batches({ ws, canManage, tabs, onOpen }: { ws: string; canManage: boolean; tabs: ReactNode; onOpen: (item: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("batches", "Imports"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Imports"
        description="Every CSV import, CRM import and send of product events is a batch: everything it wrote carries it, so a bad one can be undone in one step. Undoing shows what the batch made and what changed since first."
      />
      {tabs}
      {brand ? (
        <BatchesFor key={brand.id} brand={brand.id} canManage={canManage} onOpen={onOpen} />
      ) : (
        <Empty title="Pick a brand">
          Imports belong to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher to see its imports.
        </Empty>
      )}
    </div>
  );
}

function BatchesFor({ brand, canManage, onOpen }: { brand: string; canManage: boolean; onOpen: (item: string) => void }) {
  const [list, setList] = useState<Batch[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [kind, setKind] = useState<Batch["kind"] | "">("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  const load = useCallback(
    async (cursor?: string) => {
      const query = { limit: 25, ...(kind ? { kind } : {}), ...(cursor ? { cursor } : {}) };
      const { data, error } = await api.GET("/v1/brands/{brand}/batches", { params: { path: { brand }, query } });
      if (!data) return setError(errorMessage(error));
      setError("");
      setList((prev) => (cursor ? [...(prev ?? []), ...data.data] : data.data));
      setNext(data.next_cursor ?? null);
    },
    [brand, kind],
  );
  useEffect(() => {
    void load();
  }, [load]);
  // A batch being undone in the background: check again until it is done.
  useEffect(() => {
    if (!list?.some((b) => b.undo.status === "queued" || b.undo.status === "running")) return;
    const t = setTimeout(() => void load(), 3000);
    return () => clearTimeout(t);
  }, [list, load]);

  async function undo(b: Batch) {
    setBusy(b.id);
    const { data: p, error } = await api.GET("/v1/brands/{brand}/batches/{batch}/undo-preview", { params: { path: { brand, batch: b.id } } });
    if (!p) {
      setBusy("");
      return toast(errorMessage(error), "error");
    }
    const choice = await ask({ title: `Undo this ${batchKinds[b.kind].toLowerCase()}?`, body: <PreviewBody p={p} onOpen={onOpen} />, choices: choices(p) });
    if (!choice) return setBusy("");
    const mode = choice.choice as "untouched" | "everything";
    const res = await api.POST("/v1/brands/{brand}/batches/{batch}/undo", { params: { path: { brand, batch: b.id } }, body: { mode } });
    setBusy("");
    if (res.error) return toast(errorMessage(res.error), "error");
    if (res.response.status === 202 && res.data && "undo" in res.data) toast("Undoing in the background. This list shows when it is done.");
    else if (res.data && "undo" in res.data) toast("Undone. It is all in Recently deleted, and can be restored for 7 days.");
    else toast("Held for approval in the mission.");
    void load();
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Kind" className={field} value={kind} onChange={(e) => setKind(e.target.value as Batch["kind"] | "")}>
          <option value="">Every kind</option>
          {Object.entries(batchKinds).map(([k, label]) => <option key={k} value={k}>{label}</option>)}
        </select>
      </div>
      <ErrorText>{error}</ErrorText>
      {!list && !error && <Loading />}
      {list && list.length === 0 && <p className="text-sm text-muted-foreground">No imports{kind ? " of this kind" : ""} yet.</p>}
      {list && list.length > 0 && (
        <ol className="divide-y rounded-md border">
          {list.map((b) => (
            <li key={b.id} className="flex flex-wrap items-start gap-3 px-3 py-2.5 text-sm">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="font-medium break-words">{b.label || batchKinds[b.kind]}</span>
                  <span className="text-xs text-muted-foreground">{batchKinds[b.kind]}{b.source && b.source !== "csv" ? ` from ${b.source}` : ""}</span>
                  {b.undo.status === "done" && <Badge>Undone{b.undo.mode === "untouched" ? " (untouched only)" : ""}</Badge>}
                  {(b.undo.status === "queued" || b.undo.status === "running") && <Badge tone="amber">Undoing…</Badge>}
                  {b.undo.status === "failed" && <Badge tone="red" title={b.undo.error ?? undefined}>Undo failed</Badge>}
                </div>
                <div className="text-xs text-muted-foreground">
                  <span title={stamp(b.created_at)}>{ago(b.created_at)}</span> by {b.created_by.name ?? (b.created_by.actor_type === "api_token" ? "an API token" : "Outflare")}. Made {made(b.counts)}.
                </div>
              </div>
              {canManage && b.undo.status !== "done" && b.undo.status !== "queued" && b.undo.status !== "running" && (
                <Button variant="ghost" disabled={busy === b.id} onClick={() => undo(b)}>{busy === b.id ? "Reading…" : "Undo…"}</Button>
              )}
            </li>
          ))}
        </ol>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
    </div>
  );
}

function choices(p: Preview): { label: string; value: string; variant?: "primary" | "ghost" | "danger-solid" }[] {
  const changed = Object.values(p.changed_counts).reduce((n, c) => n + c, 0);
  const out: { label: string; value: string; variant?: "primary" | "ghost" | "danger-solid" }[] = [{ label: "Cancel", value: "cancel", variant: "ghost" }];
  if (changed > 0) out.push({ label: "Undo everything", value: "everything", variant: "danger-solid" });
  out.push({ label: changed > 0 ? "Undo only what's untouched" : "Undo", value: "untouched", variant: changed > 0 ? "primary" : "danger-solid" });
  return out;
}

function PreviewBody({ p, onOpen }: { p: Preview; onOpen: (item: string) => void }) {
  const changed = Object.values(p.changed_counts).reduce((n, c) => n + c, 0);
  const u = p.untouched;
  const e = p.everything;
  return (
    <>
      <p>It made {made(p.made)}.</p>
      {changed === 0 ? (
        <p>Nothing it made was changed since. Undoing removes all of it.</p>
      ) : (
        <>
          <p>
            Since then, {plural(changed, "thing")} changed:{" "}
            {Object.entries(p.changed_counts).map(([k, n], i) => (
              <span key={k}>{i > 0 && "; "}{n} {changedWords[k as Changed["kind"]] ?? k}</span>
            ))}
            .
          </p>
          <p>
            <strong>Undo only what's untouched</strong> removes the {plural(u.records ?? 0, "person or company", "people and companies")} nobody changed
            {u.field_values ? `, puts back ${plural(u.field_values, "field value")}` : ""}
            {u.leads ? ` and removes ${plural(u.leads, "lead")}` : ""}. The {plural(u.records_kept ?? 0, "changed record")} stay, listed below, so you can remove them by hand.
          </p>
          <p>
            <strong>Undo everything</strong> removes the changed ones too ({plural(e.records ?? 0, "person or company", "people and companies")} in all). Emails already sent
            can't be unsent: they stay in their history, marked as from an undone batch.
          </p>
        </>
      )}
      <p>Either way, it all goes to Recently deleted and can be restored for 7 days. Nothing is sent and no rule or journey fires.</p>
      {p.changed.length > 0 && (
        <ul className="max-h-40 space-y-1 overflow-y-auto rounded-md border p-2 text-xs">
          {p.changed.map((c, i) => (
            <li key={i}>
              {c.record_id ? <button type="button" className="underline" onClick={() => onOpen(c.record_id!)}>Record</button> : "Lead"}
              {c.field ? ` ${c.field}` : ""}: {changedWords[c.kind]}
              {!c.removed_by_everything && " (stays either way)"}
            </li>
          ))}
          {p.changed_truncated && <li className="text-muted-foreground">And more not listed.</li>}
        </ul>
      )}
      {p.runs_in_job && <p className="text-muted-foreground">It is large, so it is undone in the background.</p>}
    </>
  );
}
