import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import type { Schemas } from "@outflare/sdk";
import {
  CalendarDays,
  Check,
  Circle,
  FileText,
  Plus,
  Trash2,
  AlertCircle,
  Pencil,
} from "lucide-react";
import { crmApi } from "./client";
import { api, errorMessage } from "@/api";
import {
  Button,
  Empty,
  ErrorText,
  field,
  Loading,
  toast,
  confirmAction,
} from "@/ui";
import { useBrand } from "@/lib/brand";
import { useCatalog } from "@/lib/records";
import { ago, stamp } from "@/format";
import { RecordPicker, FieldInput } from "../RecordEdit";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Markdown, MarkdownEditor } from "@/components/markdown-editor";

type Activity = Schemas["CRMActivity"];
type Props = {
  ws: string;
  canManage: boolean;
  kind: "task" | "note";
  record?: string;
  myId?: string;
  onOpenRecord?: (id: string) => void;
};

export function Activities({
  ws,
  canManage,
  kind,
  record,
  myId,
  onOpenRecord,
}: Props) {
  const [rows, setRows] = useState<Activity[]>([]);
  const request = useRef(0);
  const [next, setNext] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<Activity | "new" | null>(null);
  const [status, setStatus] = useState<"open" | "done">("open");
  const [mine, setMine] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [names, setNames] = useState<Record<string, string>>({});
  const { brand } = useBrand(ws);
  const brandId = brand?.id ?? "";
  const load = useCallback(
    async (before?: string) => {
      const current = ++request.current;
      try {
        const { data, error } = await crmApi.GET(
          "/v1/brands/{brand}/crm/activities",
          {
            params: {
              path: { brand: brandId },
              query: {
                kind,
                ...(kind === "task" ? { status } : {}),
                ...(record ? { record_id: record } : {}),
                ...(mine && myId ? { assigned_to: myId } : {}),
                ...(before ? { cursor: before } : {}),
                limit: 100,
              },
            },
          },
        );
        if (current !== request.current) return;
        if (!data) {
          setError(errorMessage(error));
          return;
        }
        setRows((prev) => (before ? [...prev, ...data.data] : data.data));
        setNext(data.next_cursor ?? null);
        setError("");
      } catch (e) {
        if (current === request.current) setError(errorMessage(e));
      } finally {
        if (current === request.current) setLoaded(true);
      }
    },
    [ws, kind, record, status, mine, myId, brandId],
  );
  useEffect(() => {
    setLoaded(false);
    setRows([]);
    void load();
    return () => {
      request.current++;
    };
  }, [load]);
  useEffect(() => {
    let active = true;
    for (const record of new Set(rows.map((r) => r.record_id))) {
      if (names[record]) continue;
      api
        .GET("/v1/workspaces/{ws}/records/{record}", {
          params: { path: { ws, record } },
        })
        .then(({ data }) => {
          if (active && data)
            setNames((prev) => ({
              ...prev,
              [record]: data.name || "Unnamed record",
            }));
        })
        .catch(() => {});
    }
    return () => {
      active = false;
    };
  }, [ws, rows]); // eslint-disable-line react-hooks/exhaustive-deps
  async function toggle(row: Activity) {
    setBusy(row.id);
    try {
      const { data, error } = await crmApi.PATCH(
        "/v1/brands/{brand}/crm/activities/{id}",
        {
          params: { path: { brand: brandId, id: row.id } },
          body: {
            version: row.version,
            status: row.status === "done" ? "open" : "done",
          },
        },
      );
      if (error) {
        toast(errorMessage(error), "error");
        return;
      }
      if (!data || !("id" in data)) return;
      toast(row.status === "done" ? "Task reopened." : "Task completed.");
      void load();
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(null);
    }
  }
  async function remove(row: Activity) {
    if (
      !(await confirmAction(
        `Delete this ${kind}?`,
        "This removes it from the CRM.",
        "Delete",
      ))
    )
      return;
    setBusy(row.id);
    try {
      const { data, error } = await crmApi.DELETE(
        "/v1/brands/{brand}/crm/activities/{id}",
        { params: { path: { brand: brandId, id: row.id } } },
      );
      if (error) return toast(errorMessage(error), "error");
      if (data && "deleted" in data) {
        toast("Deleted.");
        void load();
      }
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(null);
    }
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        {kind === "task" && (
          <>
            <div className="inline-flex rounded-lg border p-0.5">
              {(["open", "done"] as const).map((s) => (
                <button
                  key={s}
                  className={`rounded-md px-3 py-1.5 text-xs font-medium ${status === s ? "bg-muted text-foreground" : "text-muted-foreground"}`}
                  onClick={() => setStatus(s)}
                >
                  {s === "open" ? "To do" : "Completed"}
                </button>
              ))}
            </div>
            {myId && (
              <label className="flex items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  checked={mine}
                  onChange={(e) => setMine(e.target.checked)}
                />
                Assigned to me
              </label>
            )}
          </>
        )}
        {canManage && (
          <Button className="ml-auto" onClick={() => setEditing("new")}>
            <Plus className="size-3.5" />
            {kind === "task" ? "Add task" : "Write a note"}
          </Button>
        )}
      </div>
      <ErrorText>{error}</ErrorText>
      {!loaded && <Loading />}
      {loaded && !error && rows.length === 0 && (
        <Empty
          title={
            kind === "task"
              ? status === "done"
                ? "No completed tasks"
                : "You're all caught up"
              : "Keep the conversation in context"
          }
        >
          {kind === "task"
            ? "Create a follow-up and link it to a person, company or deal."
            : "Add meeting notes, decisions and details your team should know."}
        </Empty>
      )}
      <div className="space-y-3">
        {rows.map((row) => {
          const overdue =
            row.status !== "done" &&
            row.due_at &&
            new Date(row.due_at).getTime() < Date.now();
          return (
            <article
              key={row.id}
              className="group rounded-xl border bg-card p-4 shadow-xs"
            >
              <div className="flex items-start gap-3">
                {kind === "task" ? (
                  <button
                    type="button"
                    disabled={!canManage || busy === row.id}
                    className={`mt-0.5 rounded-full ${row.status === "done" ? "text-emerald-600" : "text-muted-foreground hover:text-primary"}`}
                    aria-label={
                      row.status === "done" ? "Reopen task" : "Complete task"
                    }
                    onClick={() => toggle(row)}
                  >
                    {row.status === "done" ? (
                      <Check className="size-5" />
                    ) : (
                      <Circle className="size-5" />
                    )}
                  </button>
                ) : (
                  <FileText className="mt-0.5 size-4 text-muted-foreground" />
                )}
                <div className="min-w-0 flex-1">
                  <h3
                    className={`text-sm font-semibold ${row.status === "done" ? "text-muted-foreground line-through" : ""}`}
                  >
                    {row.title}
                  </h3>
                  <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    {!record && (
                      <button
                        className="underline underline-offset-2"
                        onClick={() => onOpenRecord?.(row.record_id)}
                      >
                        {names[row.record_id] ?? "Open linked record"}
                      </button>
                    )}
                    {row.due_at && (
                      <span
                        className={`inline-flex items-center gap-1 ${overdue ? "text-red-600 dark:text-red-400" : ""}`}
                      >
                        {overdue ? (
                          <AlertCircle className="size-3" />
                        ) : (
                          <CalendarDays className="size-3" />
                        )}
                        {stamp(row.due_at)}
                      </span>
                    )}
                    {row.priority === "high" && (
                      <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-700 dark:text-amber-300">
                        High priority
                      </span>
                    )}
                    <span>{ago(row.created_at)}</span>
                  </div>
                </div>
                {canManage && (
                  <div className="flex gap-1">
                    <button
                      aria-label={`Edit ${kind}`}
                      className="rounded p-1.5 text-muted-foreground hover:bg-muted"
                      onClick={() => setEditing(row)}
                    >
                      <Pencil className="size-3.5" />
                    </button>
                    <button
                      aria-label={`Delete ${kind}`}
                      disabled={busy === row.id}
                      className="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-destructive"
                      onClick={() => remove(row)}
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  </div>
                )}
              </div>
              {row.body && (
                <div className="mt-3 border-t pt-3">
                  <Markdown text={row.body} />
                </div>
              )}
            </article>
          );
        })}
      </div>
      {next && (
        <Button variant="ghost" onClick={() => load(next)}>
          Show more
        </Button>
      )}
      {editing && (
        <ActivityDialog
          ws={ws}
          kind={kind}
          record={record}
          item={editing === "new" ? undefined : editing}
          myId={myId}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function ActivityDialog({
  ws,
  kind,
  record,
  item,
  myId,
  onClose,
  onSaved,
}: {
  ws: string;
  kind: "task" | "note";
  record?: string;
  item?: Activity;
  myId?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const cat = useCatalog(ws);
  const { brand } = useBrand(ws);
  const brandId = brand?.id ?? "";
  const [title, setTitle] = useState(item?.title ?? "");
  const [body, setBody] = useState(item?.body ?? "");
  const [recordId, setRecordId] = useState(item?.record_id ?? record ?? "");
  const [type, setType] = useState("person");
  const [due, setDue] = useState(() =>
    item?.due_at
      ? new Date(
          new Date(item.due_at).getTime() -
            new Date(item.due_at).getTimezoneOffset() * 60000,
        )
          .toISOString()
          .slice(0, 16)
      : "",
  );
  const [assigned, setAssigned] = useState<unknown>(
    item?.assigned_to ?? myId ?? null,
  );
  const [priority, setPriority] = useState<Activity["priority"]>(
    item?.priority ?? "normal",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const owner: Schemas["Field"] = {
    key: "owner",
    description: "",
    option_set: null,
    target_type: null,
    label: "Assigned to",
    value_type: "actor_reference",
    multiple: false,
    write_policy: "platform_owned",
    builtin: false,
    archived: false,
    filterable: true,
    sensitivity: "internal",
    precedence: [],
  };
  async function save(e?: FormEvent) {
    e?.preventDefault();
    if (busy || !title.trim() || !recordId) return;
    setBusy(true);
    setError("");
    const input = {
      title: title.trim(),
      body,
      priority,
      assigned_to: assigned ? String(assigned) : null,
      due_at: due ? new Date(due).toISOString() : null,
    };
    try {
      const response = item
        ? await crmApi.PATCH("/v1/brands/{brand}/crm/activities/{id}", {
            params: { path: { brand: brandId, id: item.id } },
            body: { ...input, version: item.version },
          })
        : await crmApi.POST("/v1/brands/{brand}/crm/activities", {
            params: { path: { brand: brandId } },
            body: { ...input, kind, record_id: recordId },
          });
      if (response.error) {
        setError(errorMessage(response.error));
        return;
      }
      if (response.data && "id" in response.data) {
        toast(
          item
            ? "Changes saved."
            : kind === "task"
              ? "Task created."
              : "Note saved.",
        );
        onSaved();
      }
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function close() {
    if (busy) return;
    if (
      (title !== (item?.title ?? "") || body !== (item?.body ?? "")) &&
      !(await confirmAction(
        "Discard your changes?",
        "Your unsaved work will be lost.",
        "Discard",
      ))
    )
      return;
    onClose();
  }
  return (
    <Dialog open onOpenChange={(o) => !o && void close()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form className="space-y-4" onSubmit={save}>
          <DialogHeader>
            <DialogTitle>
              {item ? "Edit" : "New"} {kind}
            </DialogTitle>
            <DialogDescription>
              {kind === "task"
                ? "Give the next step an owner and a due date."
                : "Capture details alongside the relationship."}
            </DialogDescription>
          </DialogHeader>
          <label className="block text-sm font-medium">
            {kind === "task" ? "What needs to happen?" : "Title"}
            <input
              required
              autoFocus
              maxLength={300}
              className={`${field} mt-1 w-full`}
              placeholder={
                kind === "task" ? "Follow up after the demo" : "Meeting notes"
              }
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          </label>
          {!record && !item && (
            <div className="space-y-2">
              <label
                className="block text-sm font-medium"
                htmlFor="activity-record"
              >
                Linked record
              </label>
              <select
                aria-label="Record type"
                className={field}
                value={type}
                onChange={(e) => {
                  setType(e.target.value);
                  setRecordId("");
                }}
              >
                {cat.catalog?.types
                  .filter((t) => !t.archived)
                  .map((t) => (
                    <option key={t.key} value={t.key}>
                      {t.plural ?? t.name}
                    </option>
                  ))}
              </select>
              <RecordPicker
                ws={ws}
                id="activity-record"
                type={type}
                value={recordId}
                onChange={(v) => setRecordId(String(v ?? ""))}
              />
            </div>
          )}
          {kind === "task" && (
            <div className="grid gap-3 sm:grid-cols-3">
              <label className="text-sm">
                Due date
                <input
                  type="datetime-local"
                  className={`${field} mt-1 w-full`}
                  value={due}
                  onChange={(e) => setDue(e.target.value)}
                />
              </label>
              <div className="text-sm">
                <label className="mb-1 block" htmlFor="field-owner">
                  Assigned to
                </label>
                <FieldInput
                  ws={ws}
                  f={owner}
                  value={assigned}
                  names={{}}
                  cat={cat}
                  onChange={setAssigned}
                />
              </div>
              <label className="text-sm">
                Priority
                <select
                  className={`${field} mt-1 w-full`}
                  value={priority}
                  onChange={(e) =>
                    setPriority(e.target.value as Activity["priority"])
                  }
                >
                  <option value="low">Low</option>
                  <option value="normal">Normal</option>
                  <option value="high">High</option>
                </select>
              </label>
            </div>
          )}
          <MarkdownEditor
            label={kind === "task" ? "Details" : "Note"}
            value={body}
            onChange={setBody}
            minRows={6}
            placeholder="Add context…"
            onSave={() => void save()}
          />
          <ErrorText>{error}</ErrorText>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              disabled={busy}
              onClick={() => void close()}
            >
              Cancel
            </Button>
            <Button disabled={busy || !title.trim() || !recordId}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
