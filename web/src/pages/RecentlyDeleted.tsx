import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, PageHeader, confirmAction, toast } from "../ui";
import { ago, plural, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";

// Recently deleted: what was deleted from a brand's customer data in the
// last 7 days (the recovery window). Each row is one delete: a person or
// company with their emails, ids, links and events, or one field value,
// identity, link or product event. Restoring puts it back as it was and
// sends nothing again; after 7 days it is purged for good.

type Deletion = Schemas["Deletion"];
type Kind = Deletion["kind"];

const kinds: Record<Kind, string> = {
  record: "Person or company",
  field_value: "Field value",
  identity: "Email or id",
  relationship: "Link",
  event: "Product event",
};

const reasons: Record<string, string> = {
  taken: "another record or event holds this value now",
  gone: "what it points at was erased",
  changed: "the field was set again since",
  record_deleted: "its record is deleted: restore that first",
};

// left says how long until a deletion is purged.
function left(iso: string): string {
  const h = (new Date(iso).getTime() - Date.now()) / 3_600_000;
  if (h < 1) return "within the hour";
  if (h < 24) return `in ${plural(Math.ceil(h), "hour")}`;
  return `in ${plural(Math.ceil(h / 24), "day")}`;
}

export function RecentlyDeleted({ ws, canManage, tabs, onOpen }: { ws: string; canManage: boolean; tabs: ReactNode; onOpen: (item: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("deleted", "Recently deleted"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Recently deleted"
        description="What was deleted in the last 7 days. Restoring puts it back as it was, without firing rules or journeys or sending anything again. After 7 days it is gone for good. Erased people never show here: erasure is permanent at once."
      />
      {tabs}
      {brand ? (
        <DeletedFor key={brand.id} brand={brand.id} canManage={canManage} onOpen={onOpen} />
      ) : (
        <Empty title="Pick a brand">
          Deleted data belongs to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher to see what it deleted.
        </Empty>
      )}
    </div>
  );
}

function DeletedFor({ brand, canManage, onOpen }: { brand: string; canManage: boolean; onOpen: (item: string) => void }) {
  const [list, setList] = useState<Deletion[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [kind, setKind] = useState<Kind | "">("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  const load = useCallback(
    async (cursor?: string) => {
      const query = { limit: 50, ...(kind ? { kind } : {}), ...(cursor ? { cursor } : {}) };
      const { data, error } = await api.GET("/v1/brands/{brand}/recently-deleted", { params: { path: { brand }, query } });
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

  async function restore(d: Deletion) {
    const what = d.kind === "record" ? d.label || "this record" : `this ${kinds[d.kind].toLowerCase()}`;
    const body =
      d.kind === "record"
        ? "Their emails, ids, links and product events come back with them. Journeys that stopped when they were deleted stay stopped."
        : d.kind === "event"
          ? "Reports and attribution count it again. No rule or journey fires for it again."
          : "It comes back as it was.";
    if (!(await confirmAction(`Restore ${what}?`, <p>{body}</p>, "Restore", false))) return;
    setBusy(d.id);
    const { data, error } = await api.POST("/v1/brands/{brand}/recently-deleted/{deletion}/restore", { params: { path: { brand, deletion: d.id } } });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    if (data && "outcome" in data) {
      if (data.outcome === "restored") toast(`Restored ${what}.`);
      else
        toast(
          `${data.outcome === "partly_restored" ? "Partly restored" : "Not restored"}: ${plural(data.not_restored.length, "thing")} couldn't come back (${[
            ...new Set(data.not_restored.map((n) => reasons[n.reason] ?? n.reason)),
          ].join("; ")}).`,
          "error",
        );
    } else {
      toast("Held for approval in the mission.");
    }
    void load();
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Kind" className={field} value={kind} onChange={(e) => setKind(e.target.value as Kind | "")}>
          <option value="">Everything</option>
          {Object.entries(kinds).map(([k, label]) => <option key={k} value={k}>{label}</option>)}
        </select>
      </div>
      <ErrorText>{error}</ErrorText>
      {!list && !error && <Loading />}
      {list && list.length === 0 && <p className="text-sm text-muted-foreground">Nothing was deleted in the last 7 days{kind ? " of this kind" : ""}.</p>}
      {list && list.length > 0 && (
        <ol className="divide-y rounded-md border">
          {list.map((d) => {
            const rows = Object.entries(d.counts).filter(([t]) => t !== "records" && t !== "journey_runs_ended").reduce((n, [, c]) => n + c, 0);
            return (
              <li key={d.id} className="flex flex-wrap items-start gap-3 px-3 py-2.5 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-baseline gap-x-2">
                    <span className="font-medium break-words">{d.label || "Unnamed"}</span>
                    <span className="text-xs text-muted-foreground">{kinds[d.kind]}</span>
                    {d.not_restored.length > 0 && <Badge tone="amber" title={d.not_restored.map((n) => reasons[n.reason] ?? n.reason).join("; ")}>Partly restored</Badge>}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    Deleted <span title={stamp(d.deleted_at)}>{ago(d.deleted_at)}</span> by {d.deleted_by.name ?? (d.deleted_by.actor_type === "api_token" ? "an API token" : "Outflare")}
                    {d.kind === "record" && rows > 0 ? `, with ${plural(rows, "thing")} about ${d.record_type === "person" ? "them" : "it"}` : ""}. Gone for good <span title={stamp(d.purge_after)}>{left(d.purge_after)}</span>.
                  </div>
                  {d.kind !== "record" && d.record_id && (
                    <button className="text-xs underline" onClick={() => onOpen(d.record_id!)}>Open their record</button>
                  )}
                </div>
                {canManage && (
                  <Button variant="ghost" disabled={busy === d.id} onClick={() => restore(d)}>
                    {busy === d.id ? "Restoring…" : "Restore"}
                  </Button>
                )}
              </li>
            );
          })}
        </ol>
      )}
      {next && <Button variant="ghost" onClick={() => load(next)}>Show more</Button>}
    </div>
  );
}
