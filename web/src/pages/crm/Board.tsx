import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { GripVertical, Loader2 } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field, toast } from "@/ui";
import { displayName, type CatalogInfo } from "@/lib/records";
import { useBrand } from "@/lib/brand";

type Rec = Schemas["Record"];
type Query = {
  q: string;
  sort: "name" | "created_at" | "updated_at";
  direction: "asc" | "desc";
  filters: Record<string, string>;
};
export function RecordBoard({
  ws,
  type,
  cat,
  group,
  config,
  canManage,
  onOpen,
  onChange,
  tick,
}: {
  ws: string;
  type: string;
  cat: CatalogInfo;
  group: Schemas["Field"];
  config: Query;
  canManage: boolean;
  onOpen: (id: string) => void;
  onChange: () => void;
  tick: number;
}) {
  const [dragging, setDragging] = useState<Rec | null>(null);
  const [busy, setBusy] = useState(false);
  const options =
    cat.catalog?.option_sets.find((s) => s.key === group.option_set)?.options ??
    [];
  const move = async (record: Rec, value: string) => {
    if (busy || !canManage) return;
    setBusy(true);
    try {
      const { data, error } = await api.PATCH(
        "/v1/workspaces/{ws}/records/{record}",
        {
          params: { path: { ws, record: record.id } },
          body: {
            version: record.version,
            attributes: { [group.key]: value || null },
          },
        },
      );
      if (error) {
        toast(errorMessage(error), "error");
        return;
      }
      if (data && "id" in data) {
        if (data.skipped?.length) {
          toast("This value is controlled by its source.", "error");
          return;
        }
        toast("Stage updated.");
        onChange();
      }
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
      setDragging(null);
    }
  };
  // Empty values need an explicit server-side predicate, not a filter over loaded rows.
  return (
    <div className="flex items-start gap-4 overflow-x-auto pb-4">
      {[...options, { key: "", label: "No value", archived: false }].map(
        (option) => (
          <Lane
            key={option.key}
            ws={ws}
            type={type}
            group={group}
            option={option}
            options={options}
            config={config}
            canManage={canManage}
            onOpen={onOpen}
            tick={tick}
            busy={busy}
            onMove={move}
            dragging={dragging}
            onDrag={setDragging}
          />
        ),
      )}
    </div>
  );
}
function Lane({
  ws,
  type,
  group,
  option,
  options,
  config,
  canManage,
  onOpen,
  tick,
  busy,
  onMove,
  dragging,
  onDrag,
}: {
  ws: string;
  type: string;
  group: Schemas["Field"];
  option: Schemas["CatalogOption"];
  options: Schemas["CatalogOption"][];
  config: Query;
  canManage: boolean;
  onOpen: (id: string) => void;
  tick: number;
  busy: boolean;
  onMove: (r: Rec, v: string) => Promise<void>;
  dragging: Rec | null;
  onDrag: (r: Rec | null) => void;
}) {
  const { query } = useBrand(ws);
  const [rows, setRows] = useState<Rec[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const key = JSON.stringify(config);
  useEffect(() => {
    setRows([]);
    setCursor(undefined);
    setNextCursor(null);
  }, [key, tick]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    const timer = setTimeout(async () => {
      try {
        // A chosen stage filter can hide whole lanes without reading the wrong subset.
        if (
          config.filters[group.key] &&
          config.filters[group.key] !== option.key
        ) {
          setRows([]);
          setTotal(0);
          return;
        }
        const attribute = Object.entries(config.filters)
          .filter(([k, v]) => k !== group.key && v)
          .map(([k, v]) => `${k}:${v}`);
        if (option.key) attribute.push(`${group.key}:${option.key}`);
        const { data, error } = await api.GET("/v1/workspaces/{ws}/records", {
          params: {
            path: { ws },
            query: {
              type,
              q: config.q,
              sort: config.sort,
              direction: config.direction,
              cursor,
              limit: 50,
              attribute,
              ...(!option.key ? { empty_attribute: group.key } : {}),
              ...query,
            },
          },
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        if (!data) {
          setError(errorMessage(error));
          return;
        }
        setRows((prev) =>
          cursor
            ? [
                ...prev.filter((r) => !data.data.some((n) => n.id === r.id)),
                ...data.data,
              ]
            : data.data,
        );
        setNextCursor(data.next_cursor ?? null);
        setTotal(data.total ?? data.data.length);
      } catch (e) {
        if (!controller.signal.aborted) setError(errorMessage(e));
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    }, 180);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [ws, type, group.key, option.key, key, cursor, tick, query.brand]);
  return (
    <section
      className={`w-72 shrink-0 rounded-xl border bg-muted/20 ${dragging ? "border-dashed" : ""}`}
      onDragOver={(e) => {
        if (canManage && !option.archived) e.preventDefault();
      }}
      onDrop={(e) => {
        e.preventDefault();
        if (dragging && !option.archived) void onMove(dragging, option.key);
      }}
    >
      <header className="flex items-center gap-2 border-b p-3">
        <span
          className={`size-2 rounded-full ${option.key === "won" ? "bg-emerald-500" : option.key === "lost" ? "bg-red-400" : "bg-primary/50"}`}
        />
        <h3 className="text-xs font-semibold">
          {option.label}
          {option.archived ? " (archived)" : ""}
        </h3>
        <span className="ml-auto rounded bg-background px-1.5 py-0.5 text-[10px] text-muted-foreground">
          {total}
        </span>
      </header>
      <div className="min-h-28 space-y-2 p-2">
        <ErrorText>{error}</ErrorText>
        {loading && !rows.length && (
          <div role="status" className="flex justify-center p-5">
            <Loader2 className="size-4 animate-spin text-muted-foreground" />
          </div>
        )}
        {rows.map((record) => (
          <article
            key={record.id}
            draggable={canManage && !busy}
            onDragStart={(e) => {
              e.dataTransfer.setData("text/plain", record.id);
              onDrag(record);
            }}
            onDragEnd={() => onDrag(null)}
            className="rounded-lg border bg-card p-3 shadow-xs"
          >
            <button
              className="flex w-full items-start gap-2 text-left text-sm font-medium"
              aria-label={`Open ${displayName(record)}`}
              onClick={() => onOpen(record.id)}
            >
              {canManage && (
                <GripVertical className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
              )}
              <span>{displayName(record)}</span>
            </button>
            {record.attributes.value &&
            typeof record.attributes.value === "object" ? (
              <p className="mt-2 text-xs text-muted-foreground">
                {formatMoney(record.attributes.value)}
              </p>
            ) : null}
            {canManage && (
              <select
                aria-label={`Move ${displayName(record)} to stage`}
                className={`${field} mt-3 w-full text-xs`}
                disabled={busy}
                value={String(record.attributes[group.key] ?? "")}
                onChange={(e) => void onMove(record, e.target.value)}
              >
                <option value="">No value</option>
                {options
                  .filter((o) => !o.archived || o.key === option.key)
                  .map((s) => (
                    <option key={s.key} value={s.key}>
                      {s.label}
                    </option>
                  ))}
              </select>
            )}
          </article>
        ))}
        {!loading && !error && !rows.length && (
          <p className="p-3 text-center text-xs text-muted-foreground">
            {canManage && !option.archived
              ? "Drop a record here"
              : "No records"}
          </p>
        )}
        {rows.length < total && (
          <Button
            variant="ghost"
            className="w-full"
            disabled={loading || !nextCursor}
            onClick={() => setCursor(nextCursor ?? undefined)}
          >
            {loading ? "Loading…" : `Show more (${total - rows.length})`}
          </Button>
        )}
      </div>
    </section>
  );
}
function formatMoney(raw: unknown) {
  const v = raw as { amount_minor?: number; currency?: string };
  if (typeof v.amount_minor !== "number") return "";
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: v.currency ?? "USD",
    }).format(v.amount_minor / 100);
  } catch {
    return `${v.amount_minor / 100} ${v.currency ?? ""}`;
  }
}
