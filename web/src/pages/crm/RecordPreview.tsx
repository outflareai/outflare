import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import {
  Building2,
  Mail,
  Pencil,
  ExternalLink,
  User,
  ArrowUpRight,
} from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, Loading, field, toast } from "@/ui";
import {
  displayName,
  fieldLabelValue,
  identity,
  type CatalogInfo,
} from "@/lib/records";
import { ago, stamp } from "@/format";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { FieldInput, RecordDialog, editable } from "../RecordEdit";
import { Activities } from "./Activities";
import { RecordMail } from "./Mail";
import { pickedBrandId } from "@/lib/brand";

type Rec = Schemas["Record"];
export function RecordPreview({
  ws,
  id,
  cat,
  canManage,
  myId,
  onClose,
  onChange,
  onOpen,
  onFullPage,
}: {
  ws: string;
  id: string;
  cat: CatalogInfo;
  canManage: boolean;
  myId?: string;
  onClose: () => void;
  onChange: () => void;
  onOpen: (id: string) => void;
  onFullPage: () => void;
}) {
  const [record, setRecord] = useState<Rec | null>(null);
  const [links, setLinks] = useState<Schemas["RecordRelationship"][]>([]);
  const [timeline, setTimeline] = useState<Schemas["TimelineItem"][]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [section, setSection] = useState("overview");
  const [editing, setEditing] = useState(false);
  const [showEmpty, setShowEmpty] = useState(false);
  const load = useCallback(async () => {
    try {
      const [r, l, t] = await Promise.all([
        api.GET("/v1/brands/{brand}/records/{record}", {
          params: { path: { brand: pickedBrandId(), record: id } },
        }),
        api.GET("/v1/brands/{brand}/records/{record}/relationships", {
          params: { path: { brand: pickedBrandId(), record: id } },
        }),
        api.GET("/v1/brands/{brand}/records/{record}/timeline", {
          params: { path: { brand: pickedBrandId(), record: id }, query: { limit: 50 } },
        }),
      ]);
      if (!r.data) {
        setError(errorMessage(r.error));
        return;
      }
      setRecord(r.data);
      setLinks(l.data?.data ?? []);
      setTimeline(t.data?.data ?? []);
      setNext(t.data?.next_cursor ?? null);
      setError(
        l.error ? errorMessage(l.error) : t.error ? errorMessage(t.error) : "",
      );
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [ws, id]);
  useEffect(() => {
    void load();
  }, [load]);
  const names = Object.fromEntries(
    links.map((l) => [l.record.id, l.record.name || "Unnamed"]),
  );
  const changed = () => {
    void load();
    onChange();
  };
  const fields = cat.fields(record?.type ?? "person");
  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="w-full gap-0 sm:max-w-2xl">
        <SheetHeader className="border-b p-5 pr-12">
          <div className="mb-3 flex items-center gap-2 text-xs text-muted-foreground">
            <span>
              {cat.catalog?.types.find((t) => t.key === record?.type)?.name ??
                "Record"}
            </span>
            <span className="ml-auto" />
            {record && (
              <button
                className="inline-flex items-center gap-1 hover:text-foreground"
                onClick={onFullPage}
              >
                Full record
                <ArrowUpRight className="size-3.5" />
              </button>
            )}
          </div>
          <SheetTitle className="flex items-center gap-3 text-xl">
            <span className="flex size-11 shrink-0 items-center justify-center rounded-xl border bg-muted/40">
              {record?.type === "account" ? (
                <Building2 className="size-5" />
              ) : (
                <User className="size-5" />
              )}
            </span>
            {record ? displayName(record) : "Loading record…"}
          </SheetTitle>
          <SheetDescription>
            {record
              ? [
                  identity(record, "email"),
                  identity(record, "domain"),
                  record.attributes.job_title,
                ]
                  .filter(Boolean)
                  .map(String)
                  .join(" · ") || "Your team's shared relationship record"
              : "Reading record details"}
          </SheetDescription>
          {record && (
            <div className="mt-3 flex flex-wrap gap-2">
              {canManage && !record.archived && (
                <Button variant="ghost" onClick={() => setEditing(true)}>
                  <Pencil className="size-3.5" />
                  Edit
                </Button>
              )}
              {identity(record, "email") && (
                <a
                  className={`${field} inline-flex items-center gap-1.5`}
                  href={`mailto:${identity(record, "email")}`}
                >
                  <Mail className="size-3.5" />
                  Email
                </a>
              )}
              {canManage && (
                <>
                  <Button variant="ghost" onClick={() => setSection("notes")}>
                    Notes
                  </Button>
                  <Button variant="ghost" onClick={() => setSection("tasks")}>
                    Tasks
                  </Button>
                </>
              )}
            </div>
          )}
        </SheetHeader>
        <div
          className="flex border-b px-5"
          role="tablist"
          aria-label="Record sections"
        >
          {["overview", "activity", "email", "notes", "tasks"].map((key) => (
            <button
              key={key[0].toUpperCase() + key.slice(1)}
              type="button"
              role="tab"
              aria-selected={section === key}
              className={`border-b-2 px-3 py-3 text-xs font-medium capitalize ${section === key ? "border-primary text-foreground" : "border-transparent text-muted-foreground"}`}
              onClick={() => setSection(key)}
            >
              {key}
            </button>
          ))}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-5">
          <ErrorText>{error}</ErrorText>
          {!record && !error && <Loading />}
          {record && section === "overview" && (
            <div className="space-y-6">
              <div>
                <h3 className="mb-3 text-xs font-semibold text-muted-foreground">
                  PROPERTIES
                </h3>
                <div className="divide-y rounded-xl border">
                  {fields
                    .filter(
                      (f) =>
                        showEmpty ||
                        ["owner", "lifecycle_stage", "stage"].includes(f.key) ||
                        (record.attributes[f.key] != null &&
                          record.attributes[f.key] !== "" &&
                          (!Array.isArray(record.attributes[f.key]) ||
                            (record.attributes[f.key] as unknown[]).length >
                              0)),
                    )
                    .map((f) => (
                      <div
                        key={f.key}
                        className="grid grid-cols-[minmax(100px,1fr)_2fr] items-start gap-3 px-3 py-2.5 text-sm"
                      >
                        <span className="pt-1 text-xs text-muted-foreground">
                          {f.label}
                        </span>
                        <InlineField
                          ws={ws}
                          record={record}
                          f={f}
                          cat={cat}
                          names={names}
                          canManage={canManage}
                          onSaved={changed}
                        />
                      </div>
                    ))}
                </div>
              </div>
              <button
                className="text-xs text-muted-foreground underline"
                onClick={() => setShowEmpty((v) => !v)}
              >
                {showEmpty ? "Hide empty properties" : "Show all properties"}
              </button>
              {links.length > 0 && (
                <div>
                  <h3 className="mb-3 text-xs font-semibold text-muted-foreground">
                    RELATIONSHIPS
                  </h3>
                  <div className="divide-y rounded-xl border">
                    {links.map((link) => (
                      <button
                        key={link.id}
                        className="flex w-full items-center gap-3 px-3 py-3 text-left text-sm hover:bg-muted/40"
                        onClick={() => onOpen(link.record.id)}
                      >
                        <Building2 className="size-4 text-muted-foreground" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-medium">
                            {link.record.name || "Unnamed"}
                          </span>
                          <span className="text-xs text-muted-foreground">
                            {link.field_key ?? link.kind.replaceAll("_", " ")}
                          </span>
                        </span>
                        <ExternalLink className="size-3.5 text-muted-foreground" />
                      </button>
                    ))}
                  </div>
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                Added {ago(record.created_at)} · Updated{" "}
                {ago(record.updated_at)}
              </p>
            </div>
          )}
          {record && section === "activity" && (
            <div className="space-y-3">
              {timeline.length === 0 && (
                <p className="py-8 text-center text-sm text-muted-foreground">
                  Activity will appear here as the relationship develops.
                </p>
              )}
              {timeline.map((item) => (
                <article
                  key={`${item.kind}:${item.id}`}
                  className="rounded-xl border p-3"
                >
                  <div className="flex items-baseline justify-between gap-3">
                    <h3 className="text-sm font-medium">
                      {item.title || item.kind.replaceAll("_", " ")}
                    </h3>
                    <time
                      className="shrink-0 text-xs text-muted-foreground"
                      title={stamp(item.at)}
                    >
                      {ago(item.at)}
                    </time>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {item.kind.replaceAll("_", " ")}
                    {item.source ? ` · ${item.source}` : ""}
                  </p>
                  {item.kind === "attribute_change" && (
                    <p className="mt-2 text-xs">
                      {String(
                        (item.detail as Record<string, unknown>).old_value ??
                          "Empty",
                      )}{" "}
                      →{" "}
                      {String(
                        (item.detail as Record<string, unknown>).new_value ??
                          "Empty",
                      )}
                    </p>
                  )}
                </article>
              ))}
              {next && (
                <Button
                  variant="ghost"
                  onClick={async () => {
                    const { data, error } = await api.GET(
                      "/v1/brands/{brand}/records/{record}/timeline",
                      {
                        params: {
                          path: { brand: record.brand_id ?? pickedBrandId(), record: id },
                          query: { cursor: next, limit: 50 },
                        },
                      },
                    );
                    if (error) return toast(errorMessage(error), "error");
                    if (data) {
                      setTimeline((prev) => [...prev, ...data.data]);
                      setNext(data.next_cursor ?? null);
                    }
                  }}
                >
                  Show more
                </Button>
              )}
            </div>
          )}
          {record && section === "email" && (
            <RecordMail
              key={id}
              ws={ws}
              record={id}
              recordType={record.type}
              myId={myId}
              canManage={canManage}
            />
          )}
          {record && (section === "notes" || section === "tasks") && (
            <Activities
              key={`${id}:${section}`}
              ws={ws}
              record={id}
              kind={section === "tasks" ? "task" : "note"}
              canManage={canManage}
              myId={myId}
            />
          )}
        </div>
        {record && editing && (
          <RecordDialog
            ws={ws}
            type={record.type}
            rec={record}
            cat={cat}
            names={names}
            onClose={() => setEditing(false)}
            onSaved={changed}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

export function InlineField({
  ws,
  record,
  f,
  cat,
  names = {},
  canManage,
  onSaved,
}: {
  ws: string;
  record: Rec;
  f: Schemas["Field"];
  cat: CatalogInfo;
  names?: Record<string, string>;
  canManage: boolean;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState<unknown>(record.attributes[f.key] ?? null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const label = fieldLabelValue(
    cat,
    record.type,
    f.key,
    record.attributes[f.key],
    names,
  );
  const canEdit =
    canManage && editable(f) && !record.archived && !record.merged_into;
  if (!canEdit)
    return (
      <span
        className="block break-words py-1 text-xs"
        title={
          f.write_policy === "computed" ? "Calculated automatically" : undefined
        }
      >
        {label || "—"}
      </span>
    );
  async function save() {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const { data, error } = await api.PATCH(
        "/v1/brands/{brand}/records/{record}",
        {
          params: { path: { brand: record.brand_id ?? pickedBrandId(), record: record.id } },
          body: { version: record.version, attributes: { [f.key]: value } },
        },
      );
      if (error) {
        setError(errorMessage(error));
        return;
      }
      if (data && "id" in data) {
        if (data.skipped?.length) {
          setError(
            "This value is controlled by its source and was kept unchanged.",
          );
          return;
        }
        setOpen(false);
        onSaved();
        toast("Saved.");
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Popover
      open={open}
      onOpenChange={(v) => {
        if (busy) return;
        setOpen(v);
        if (v) {
          setValue(record.attributes[f.key] ?? null);
          setError("");
        }
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`Edit ${f.label}`}
          className="group/field flex min-h-7 w-full items-center gap-2 rounded px-1.5 py-1 text-left text-xs hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring"
        >
          <span
            className={`min-w-0 flex-1 break-words ${label ? "" : "text-muted-foreground"}`}
          >
            {label || "Set value…"}
          </span>
          <Pencil className="size-3 shrink-0 text-muted-foreground opacity-0 group-hover/field:opacity-100" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80">
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <label
            htmlFor={`field-${f.key}`}
            className="block text-sm font-medium"
          >
            {f.label}
          </label>
          <FieldInput
            ws={ws}
            f={f}
            value={value}
            names={names}
            cat={cat}
            onChange={setValue}
          />
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
            <Button disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}
