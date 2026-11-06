import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import type { Schemas } from "@outflare/sdk";
import {
  ArrowDown,
  ArrowUp,
  Building2,
  CheckSquare,
  ChevronLeft,
  ChevronRight,
  Columns3,
  Download,
  FileText,
  Filter,
  LayoutGrid,
  List,
  Plus,
  Search,
  Settings2,
  Users,
  X,
  Bookmark,
  Trash2,
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
import { setBrand, useBrand } from "@/lib/brand";
import { displayName, identity, useCatalog } from "@/lib/records";
import { track } from "@/lib/analytics";
import type { Tab } from "@/App";
import { RecordDialog, FieldInput, RecordPicker } from "../RecordEdit";
import { Activities } from "./Activities";
import { InlineField, RecordPreview } from "./RecordPreview";
import { RecordBoard } from "./Board";
import { CRMReports } from "./Reports";
import { CRMCollections } from "./Collections";
import { CRMSettings } from "./Settings";
import { MailPrivacy } from "./Mail";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Rec = Schemas["Record"];
type View = Schemas["CRMView"];
type Config = {
  q: string;
  sort: "name" | "created_at" | "updated_at";
  direction: "asc" | "desc";
  filters: Record<string, string>;
  columns: string[] | null;
  layout: "table" | "board";
  group: string;
};
const defaults = (): Config => ({
  q: "",
  sort: "created_at",
  direction: "desc",
  filters: {},
  columns: null,
  layout: "table",
  group: "",
});
function readConfig(raw: unknown): Config {
  const c = raw && typeof raw === "object" ? (raw as Partial<Config>) : {};
  return {
    q: typeof c.q === "string" ? c.q : "",
    sort: ["name", "created_at", "updated_at"].includes(c.sort ?? "")
      ? c.sort!
      : "created_at",
    direction: c.direction === "asc" ? "asc" : "desc",
    filters:
      c.filters && typeof c.filters === "object" && !Array.isArray(c.filters)
        ? Object.fromEntries(
            Object.entries(c.filters).filter(([, v]) => typeof v === "string"),
          )
        : {},
    columns: Array.isArray(c.columns)
      ? [...new Set(c.columns.filter((v) => typeof v === "string"))]
      : null,
    layout: c.layout === "board" ? "board" : "table",
    group: typeof c.group === "string" ? c.group : "",
  };
}

export function CRMWorkspace({
  ws,
  canManage,
  canConfigure,
  canManageMailPolicy = false,
  myId,
  open,
  setOpen,
  go,
}: {
  ws: string;
  canManage: boolean;
  canConfigure: boolean;
  canManageMailPolicy?: boolean;
  myId?: string;
  open?: string;
  setOpen: (item?: string) => void;
  go: (tab: Tab, item?: string) => void;
}) {
  const { brand, active } = useBrand(ws);
  if (!brand)
    return (
      <Empty title="Choose a brand to open its sales workspace">
        <p className="mb-4">
          Keep each brand’s relationships and sales work together.
        </p>
        <div className="flex flex-wrap justify-center gap-2">
          {active.map((b) => (
            <Button key={b.id} onClick={() => setBrand(b.handle)}>
              {b.name}
            </Button>
          ))}
        </div>
      </Empty>
    );
  return (
    <Workspace
      key={`${ws}:${brand?.id ?? "all"}`}
      ws={ws}
      canManage={canManage}
      canManageMailPolicy={canManageMailPolicy}
      canConfigure={canConfigure}
      myId={myId}
      open={open}
      setOpen={setOpen}
      go={go}
    />
  );
}
function Workspace({
  ws,
  canManage,
  canConfigure,
  canManageMailPolicy = false,
  myId,
  open,
  setOpen,
  go,
}: Parameters<typeof CRMWorkspace>[0]) {
  const [catalogTick, setCatalogTick] = useState(0);
  return (
    <WorkspaceBody
      key={catalogTick}
      ws={ws}
      canManage={canManage}
      canConfigure={canConfigure}
      canManageMailPolicy={canManageMailPolicy}
      myId={myId}
      open={open}
      setOpen={setOpen}
      go={go}
      reloadCatalog={() => setCatalogTick((n) => n + 1)}
    />
  );
}
function WorkspaceBody({
  ws,
  canManage,
  canConfigure,
  canManageMailPolicy = false,
  myId,
  open,
  setOpen,
  go,
  reloadCatalog,
}: Parameters<typeof CRMWorkspace>[0] & { reloadCatalog: () => void }) {
  const cat = useCatalog(ws);
  const { brand, query } = useBrand(ws);
  const brandId = brand?.id ?? "";
  const parts = (open ?? "person").split(":");
  const section = parts[0] || "person";
  const preview = parts[1];
  const special = [
    "tasks",
    "notes",
    "settings",
    "reports",
    "collections",
  ].includes(section);
  const type = special ? "person" : section;
  const obj = cat.catalog?.types.find((t) => t.key === type);
  const fields = cat.fields(type);
  const [config, setConfig] = useState<Config>(() => ({
    ...defaults(),
    ...(type === "deal" ? { layout: "board", group: "stage" } : {}),
  }));
  const [rows, setRows] = useState<Rec[]>([]);
  const [referenceNames, setReferenceNames] = useState<Record<string, string>>(
    {},
  );
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const pageSize = 50;
  const cursor = cursors[offset / pageSize];
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [views, setViews] = useState<View[]>([]);
  const [viewId, setViewId] = useState("");
  const [savingView, setSavingView] = useState(false);
  const [viewName, setViewName] = useState("");
  const [shared, setShared] = useState(false);
  const [viewError, setViewError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);
  const request = useRef(0);
  const [bulkField, setBulkField] = useState("");
  const [bulkValue, setBulkValue] = useState<unknown>(null);
  const [bulkOpen, setBulkOpen] = useState(false);
  const configKey = JSON.stringify({
    q: config.q,
    sort: config.sort,
    direction: config.direction,
    filters: config.filters,
  });
  const viewRequest = useRef(0);
  const loadViews = useCallback(async () => {
    const current = ++viewRequest.current;
    try {
      const { data, error } = await crmApi.GET("/v1/brands/{brand}/crm/views", {
        params: {
          path: { brand: brandId },
          query: { record_type: type, limit: 500 },
        },
      });
      if (current !== viewRequest.current) return;
      if (data) setViews(data.data);
      else setViewError(errorMessage(error));
    } catch (e) {
      if (current === viewRequest.current) setViewError(errorMessage(e));
    }
  }, [ws, type, query.brand]);
  useEffect(() => {
    void loadViews();
    return () => {
      viewRequest.current++;
    };
  }, [loadViews]);
  useEffect(() => {
    setConfig({
      ...defaults(),
      ...(type === "deal" ? { layout: "board", group: "stage" } : {}),
    });
    setOffset(0);
    setCursors([undefined]);
    setNextCursor(null);
    setSelected(new Set());
    setViewId("");
  }, [type]);
  useEffect(() => {
    if (special) return;
    const controller = new AbortController();
    const version = ++request.current;
    setLoading(true);
    setError("");
    setSelected(new Set());
    const timer = setTimeout(async () => {
      try {
        if (!brandId) return;
        const { data, error } = await api.GET("/v1/brands/{brand}/records", {
          params: {
            path: { brand: brandId },
            query: {
              type,
              limit: pageSize,
              cursor,
              q: config.q,
              sort: config.sort,
              direction: config.direction,
              attribute: Object.entries(config.filters)
                .filter(([, v]) => v)
                .map(([k, v]) => `${k}:${v}`),
            },
          },
          signal: controller.signal,
        });
        if (controller.signal.aborted || request.current !== version) return;
        if (!data) {
          setError(errorMessage(error));
          return;
        }
        setRows(data.data);
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
  }, [ws, type, special, configKey, offset, cursor, query.brand, tick]); // config includes table and board preferences; server applies all data filters.
  const change = (patch: Partial<Config>) => {
    setConfig((prev) => ({ ...prev, ...patch }));
    if (
      patch.q !== undefined ||
      patch.sort !== undefined ||
      patch.direction !== undefined ||
      patch.filters !== undefined
    )
      setOffset(0);
      setCursors([undefined]);
      setNextCursor(null);
    if (patch.layout) setSelected(new Set());
    track("ui.crm_view_changed", {
      layout: patch.layout,
      filters: patch.filters ? Object.keys(patch.filters).length : undefined,
    });
  };
  const refresh = () => setTick((n) => n + 1);
  const openRecord = (id: string) => setOpen(`${section}:${id}`);
  const activeView = views.find((v) => v.id === viewId);
  const defaultColumns =
    type === "person"
      ? ["job_title", "companies", "lifecycle_stage", "owner"]
      : type === "account"
        ? ["industry", "company_size", "lifecycle_stage", "owner"]
        : type === "deal"
          ? ["stage", "value", "associated_company", "close_date"]
          : fields.slice(0, 5).map((f) => f.key);
  const activeColumns = (config.columns ?? defaultColumns).filter((key) =>
    fields.some((f) => f.key === key),
  );
  const boardFields = fields.filter(
    (f) =>
      (f.value_type === "status" || f.value_type === "select") &&
      !f.multiple &&
      f.option_set,
  );
  const group =
    boardFields.find((f) => f.key === config.group) ?? boardFields[0];
  useEffect(() => {
    const ids = [
      ...new Set(
        rows.flatMap((r) =>
          fields
            .filter((f) => f.value_type === "record_reference")
            .flatMap((f) => {
              const v = r.attributes[f.key];
              return Array.isArray(v) ? v.map(String) : v ? [String(v)] : [];
            }),
        ),
      ),
    ].slice(0, 200);
    if (!ids.length || !brandId) {
      setReferenceNames({});
      return;
    }
    const controller = new AbortController();
    api
      .GET("/v1/brands/{brand}/records", {
        params: { path: { brand: brandId }, query: { ids, limit: 200 } },
        signal: controller.signal,
      })
      .then(({ data }) => {
        if (!controller.signal.aborted && data)
          setReferenceNames(
            Object.fromEntries(data.data.map((r) => [r.id, displayName(r)])),
          );
      })
      .catch(() => {});
    return () => controller.abort();
  }, [ws, rows, cat.catalog, query.brand]);
  const chosen = rows.filter((r) => selected.has(r.id));
  async function saveView(e: FormEvent) {
    e.preventDefault();
    if (!brand || busy) return;
    setBusy(true);
    setViewError("");
    try {
      const { data, error } = await crmApi.POST(
        "/v1/brands/{brand}/crm/views",
        {
          params: { path: { brand: brandId } },
          body: {
            brand_id: brand.id,
            name: viewName.trim(),
            record_type: type,
            shared,
            config: { ...config },
          },
        },
      );
      if (error) {
        setViewError(errorMessage(error));
        return;
      }
      if (data && "id" in data) {
        setViewId(data.id);
        setSavingView(false);
        setViewName("");
        void loadViews();
        toast("View saved.");
      }
    } catch (e) {
      setViewError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function updateView() {
    if (!activeView || busy) return;
    setBusy(true);
    try {
      const { data, error } = await crmApi.PATCH(
        "/v1/brands/{brand}/crm/views/{id}",
        {
          params: { path: { brand: brandId, id: activeView.id } },
          body: { version: activeView.version, config: { ...config } },
        },
      );
      if (error) return toast(errorMessage(error), "error");
      if (data && "id" in data) {
        void loadViews();
        toast("View updated.");
      }
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  }
  async function deleteView() {
    if (
      !activeView ||
      !(await confirmAction(
        "Delete this saved view?",
        "The records stay in your CRM.",
        "Delete",
      ))
    )
      return;
    try {
      const { data, error } = await crmApi.DELETE(
        "/v1/brands/{brand}/crm/views/{id}",
        { params: { path: { brand: brandId, id: activeView.id } } },
      );
      if (error) return toast(errorMessage(error), "error");
      if (data && "deleted" in data) {
        setViewId("");
        void loadViews();
        toast("View deleted.");
      }
    } catch (e) {
      toast(errorMessage(e), "error");
    }
  }
  async function bulk(archive = false) {
    if (busy || !chosen.length) return;
    if (
      archive &&
      !(await confirmAction(
        `Archive ${chosen.length} records?`,
        "You can restore them from the record directory.",
        "Archive",
      ))
    )
      return;
    setBusy(true);
    let changed = 0;
    let failures = 0;
    for (const row of chosen) {
      try {
        const { data, error } = await api.PATCH(
          "/v1/brands/{brand}/records/{record}",
          {
            params: { path: { brand: row.brand_id ?? brandId, record: row.id } },
            body: {
              version: row.version,
              ...(archive
                ? { archived: true }
                : { attributes: { [bulkField]: bulkValue } }),
            },
          },
        );
        if (error || !data || !("id" in data) || data.skipped?.length) {
          failures++;
        } else changed++;
      } catch {
        failures++;
      }
    }
    setBusy(false);
    setBulkOpen(false);
    refresh();
    toast(
      `${changed} records ${archive ? "archived" : "updated"}.${failures ? ` ${failures} could not be changed; refresh and try again.` : ""}`,
      failures ? "error" : undefined,
    );
  }
  function exportRows() {
    const safe = (v: string) =>
      `"${(/^[=+@\-\t\r]/.test(v) ? "'" : "") + v.replaceAll('"', '""')}"`;
    const lines = [
      [
        "Name",
        "Email / domain",
        ...activeColumns.map((key) => fields.find((f) => f.key === key)!.label),
      ],
      ...rows.map((r) => [
        displayName(r),
        identity(r, type === "account" ? "domain" : "email") ?? "",
        ...activeColumns.map((key) => {
          const value = r.attributes[key];
          return value == null
            ? ""
            : typeof value === "object"
              ? JSON.stringify(value)
              : String(value);
        }),
      ]),
    ];
    const blob = new Blob(
      [lines.map((line) => line.map(safe).join(",")).join("\r\n")],
      { type: "text/csv;charset=utf-8" },
    );
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${type}-current-page.csv`;
    a.click();
    URL.revokeObjectURL(url);
    track("ui.crm_exported", { count: rows.length });
  }
  const navigate = (value: string) => {
    setOpen(value);
    track("ui.crm_section_opened", {
      section: [
        "person",
        "account",
        "deal",
        "tasks",
        "notes",
        "settings",
      ].includes(value)
        ? value
        : "custom",
    });
  };
  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="mb-1 text-xs text-muted-foreground">
            {brand?.name ?? "All brands"} / CRM
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">
            {section === "tasks"
              ? "My work"
              : section === "notes"
                ? "Notes"
                : section === "settings"
                  ? "CRM settings"
                  : section === "reports"
                    ? "Sales overview"
                    : section === "collections"
                      ? "Lists"
                      : (obj?.plural ?? "Relationships")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {section === "tasks"
              ? "Keep every follow-up moving."
              : section === "notes"
                ? "The context your team needs, connected to each relationship."
                : section === "settings"
                  ? "Make the CRM fit the way your team works."
                  : "Your relationships, connected. Every detail in one place."}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <div className="min-w-52">
            <RecordPicker
              ws={ws}
              type=""
              value={null}
              onChange={() => {}}
              onSelect={(record) => openRecord(record.id)}
              placeholder="Search all records…"
            />
          </div>
          <Button variant="ghost" onClick={() => go("connectors")}>
            Connect data
          </Button>
          {!special && canManage && obj && (
            <Button onClick={() => setAdding(true)}>
              <Plus className="size-3.5" />
              Add {obj.name.toLowerCase()}
            </Button>
          )}
        </div>
      </header>
      <nav
        aria-label="CRM sections"
        className="flex flex-wrap gap-1 rounded-xl border bg-card p-1.5 shadow-xs"
      >
        {[
          { key: "person", label: "People", Icon: Users },
          { key: "account", label: "Companies", Icon: Building2 },
          { key: "deal", label: "Deals", Icon: LayoutGrid },
          { key: "tasks", label: "My work", Icon: CheckSquare },
          { key: "notes", label: "Notes", Icon: FileText },
          ...(
            cat.catalog?.types.filter(
              (t) =>
                !["person", "account", "deal"].includes(t.key) &&
                !t.archived &&
                !t.key.startsWith("list_"),
            ) ?? []
          ).map((t) => ({ key: t.key, label: t.plural, Icon: List })),
          { key: "collections", label: "Lists", Icon: List },
          { key: "reports", label: "Reports", Icon: LayoutGrid },
          { key: "settings", label: "Settings", Icon: Settings2 },
        ].map(({ key, label, Icon }) => (
          <button
            key={key}
            className={`inline-flex items-center gap-2 rounded-lg px-3 py-2 text-xs font-medium transition-colors ${section === key ? "bg-muted text-foreground" : "text-muted-foreground hover:bg-muted/50 hover:text-foreground"}`}
            aria-current={section === key ? "page" : undefined}
            onClick={() => navigate(key)}
          >
            <Icon className="size-3.5" />
            {label}
          </button>
        ))}
      </nav>
      <ErrorText>{cat.error}</ErrorText>
      {!cat.catalog && !cat.error && <Loading />}
      {special &&
        section !== "settings" &&
        section !== "reports" &&
        section !== "collections" && (
          <Activities
            key={section}
            ws={ws}
            kind={section === "tasks" ? "task" : "note"}
            canManage={canManage}
            myId={myId}
            onOpenRecord={openRecord}
          />
        )}
      {section === "collections" && (
        <CRMCollections
          ws={ws}
          cat={cat}
          canConfigure={canConfigure}
          onOpen={navigate}
          onReload={reloadCatalog}
        />
      )}
      {section === "reports" && (
        <CRMReports
          ws={ws}
          cat={cat}
          onOpen={openRecord}
          onTasks={() => navigate("tasks")}
        />
      )}
      {section === "settings" && (
        <MailPrivacy
          ws={ws}
          canManage={canManage}
          canManagePolicy={canManageMailPolicy}
        />
      )}
      {section === "settings" && (
        <CRMSettings
          ws={ws}
          cat={cat}
          canManage={canConfigure}
          reload={reloadCatalog}
        />
      )}
      {!special && cat.catalog && !obj && (
        <Empty title="Object not found">
          Choose an object above to continue.
        </Empty>
      )}
      {!special && obj && (
        <>
          <div className="flex flex-wrap items-center gap-2 border-b pb-3">
            <select
              aria-label="Saved view"
              className={`${field} max-w-60 font-medium`}
              value={viewId}
              onChange={(e) => {
                const v = views.find((v) => v.id === e.target.value);
                setViewId(e.target.value);
                setConfig(v ? readConfig(v.config) : defaults());
                setOffset(0);
                setCursors([undefined]);
                setNextCursor(null);
              }}
            >
              <option value="">All {obj.plural.toLowerCase()}</option>
              {views.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name}
                  {v.shared ? " · Team" : " · Personal"}
                </option>
              ))}
            </select>
            {canManage && brand && (
              <Button
                variant="ghost"
                onClick={() => {
                  setSavingView(true);
                  setViewError("");
                }}
              >
                <Bookmark className="size-3.5" />
                Save view
              </Button>
            )}
            {canManage && activeView?.created_by === myId && (
              <>
                <Button variant="ghost" disabled={busy} onClick={updateView}>
                  Update view
                </Button>
                <button
                  className="rounded p-2 text-muted-foreground hover:text-destructive"
                  aria-label="Delete saved view"
                  onClick={deleteView}
                >
                  <Trash2 className="size-3.5" />
                </button>
              </>
            )}
            <div className="ml-auto inline-flex rounded-lg border p-0.5">
              <button
                aria-label="Table view"
                aria-pressed={config.layout === "table"}
                className={`rounded-md p-1.5 ${config.layout === "table" ? "bg-muted" : "text-muted-foreground"}`}
                onClick={() => change({ layout: "table" })}
              >
                <List className="size-4" />
              </button>
              <button
                aria-label="Board view"
                aria-pressed={config.layout === "board"}
                disabled={!boardFields.length}
                title={
                  !boardFields.length
                    ? "Add a status or select attribute to use a board"
                    : undefined
                }
                className={`rounded-md p-1.5 disabled:opacity-30 ${config.layout === "board" ? "bg-muted" : "text-muted-foreground"}`}
                onClick={() => change({ layout: "board" })}
              >
                <LayoutGrid className="size-4" />
              </button>
            </div>
          </div>
          <ErrorText>{viewError}</ErrorText>
          <div className="flex flex-wrap items-center gap-2">
            <label className="relative min-w-48 flex-1">
              <Search className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                aria-label={`Search ${obj.plural.toLowerCase()}`}
                type="search"
                className={`${field} w-full pl-8`}
                placeholder={`Search ${obj.plural.toLowerCase()}…`}
                value={config.q}
                onChange={(e) => change({ q: e.target.value })}
              />
            </label>
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="ghost">
                  <Filter className="size-3.5" />
                  Filters
                  {Object.values(config.filters).filter(Boolean).length > 0
                    ? ` (${Object.values(config.filters).filter(Boolean).length})`
                    : ""}
                </Button>
              </PopoverTrigger>
              <PopoverContent
                align="end"
                className="max-h-[70vh] w-80 space-y-3 overflow-y-auto"
              >
                <p className="text-sm font-semibold">Match these attributes</p>
                {fields
                  .filter(
                    (f) =>
                      f.filterable &&
                      [
                        "select",
                        "status",
                        "actor_reference",
                        "record_reference",
                      ].includes(f.value_type),
                  )
                  .map((f) => (
                    <div key={f.key}>
                      <label
                        htmlFor={`filter-${f.key}`}
                        className="mb-1 block text-xs"
                      >
                        {f.label}
                      </label>
                      {f.option_set ? (
                        <select
                          id={`filter-${f.key}`}
                          className={`${field} w-full`}
                          value={config.filters[f.key] ?? ""}
                          onChange={(e) =>
                            change({
                              filters: {
                                ...config.filters,
                                [f.key]: e.target.value,
                              },
                            })
                          }
                        >
                          <option value="">Any value</option>
                          {cat.options(f).map((o) => (
                            <option key={o.key} value={o.key}>
                              {o.label}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <FieldInput
                          ws={ws}
                          f={{ ...f, multiple: false, key: `filter-${f.key}` }}
                          cat={cat}
                          names={{}}
                          value={config.filters[f.key]}
                          onChange={(v) =>
                            change({
                              filters: {
                                ...config.filters,
                                [f.key]: String(v ?? ""),
                              },
                            })
                          }
                        />
                      )}
                    </div>
                  ))}
                <Button variant="ghost" onClick={() => change({ filters: {} })}>
                  Clear filters
                </Button>
              </PopoverContent>
            </Popover>
            <select
              aria-label="Sort records"
              className={field}
              value={config.sort}
              onChange={(e) =>
                change({ sort: e.target.value as Config["sort"] })
              }
            >
              <option value="created_at">Date added</option>
              <option value="updated_at">Last updated</option>
              <option value="name">Name</option>
            </select>
            <button
              aria-label={
                config.direction === "asc"
                  ? "Sort descending"
                  : "Sort ascending"
              }
              className={`${field} p-2`}
              onClick={() =>
                change({
                  direction: config.direction === "asc" ? "desc" : "asc",
                })
              }
            >
              {config.direction === "asc" ? (
                <ArrowUp className="size-3.5" />
              ) : (
                <ArrowDown className="size-3.5" />
              )}
            </button>
            {config.layout === "board" && (
              <select
                aria-label="Group board by"
                className={field}
                value={group?.key ?? ""}
                onChange={(e) => change({ group: e.target.value })}
              >
                {boardFields.map((f) => (
                  <option key={f.key} value={f.key}>
                    {f.label}
                  </option>
                ))}
              </select>
            )}
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="ghost">
                  <Columns3 className="size-3.5" />
                  Columns
                </Button>
              </PopoverTrigger>
              <PopoverContent
                align="end"
                className="max-h-96 w-64 overflow-y-auto"
              >
                <p className="mb-2 text-sm font-semibold">Visible attributes</p>
                {[
                  ...activeColumns,
                  ...fields
                    .map((f) => f.key)
                    .filter((key) => !activeColumns.includes(key)),
                ].map((key) => {
                  const f = fields.find((f) => f.key === key)!;
                  const index = activeColumns.indexOf(key);
                  const move = (direction: number) => {
                    const columns = [...activeColumns];
                    [columns[index], columns[index + direction]] = [
                      columns[index + direction],
                      columns[index],
                    ];
                    change({ columns });
                  };
                  return (
                    <div
                      key={key}
                      className="flex items-center gap-1 rounded px-2 py-1.5 text-xs hover:bg-muted"
                    >
                      <label className="flex flex-1 items-center gap-2">
                        <input
                          type="checkbox"
                          checked={index >= 0}
                          onChange={(e) =>
                            change({
                              columns: e.target.checked
                                ? [...activeColumns, key]
                                : activeColumns.filter((k) => k !== key),
                            })
                          }
                        />
                        {f.label}
                      </label>
                      {index >= 0 && (
                        <>
                          <button
                            type="button"
                            className="rounded p-1 hover:bg-background disabled:opacity-30"
                            aria-label={`Move ${f.label} left`}
                            disabled={index === 0}
                            onClick={() => move(-1)}
                          >
                            <ArrowUp className="size-3" />
                          </button>
                          <button
                            type="button"
                            className="rounded p-1 hover:bg-background disabled:opacity-30"
                            aria-label={`Move ${f.label} right`}
                            disabled={index === activeColumns.length - 1}
                            onClick={() => move(1)}
                          >
                            <ArrowDown className="size-3" />
                          </button>
                        </>
                      )}
                    </div>
                  );
                })}
              </PopoverContent>
            </Popover>
            <button
              aria-label="Export current page"
              title="Export the records on this page"
              className={`${field} p-2`}
              disabled={loading || !rows.length}
              onClick={exportRows}
            >
              <Download className="size-3.5" />
            </button>
          </div>
          {Object.values(config.filters).some(Boolean) && (
            <div className="flex flex-wrap gap-2">
              {Object.entries(config.filters)
                .filter(([, v]) => v)
                .map(([key, value]) => (
                  <button
                    key={key}
                    className="inline-flex items-center gap-1.5 rounded-full border bg-muted/30 px-2.5 py-1 text-xs"
                    onClick={() =>
                      change({ filters: { ...config.filters, [key]: "" } })
                    }
                  >
                    {fields.find((f) => f.key === key)?.label ?? key}:{" "}
                    {cat.labels
                      .get(fields.find((f) => f.key === key)?.option_set ?? "")
                      ?.get(value) ?? "Selected record"}
                    <X className="size-3" />
                  </button>
                ))}
            </div>
          )}
          {chosen.length > 0 && (
            <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/40 p-3 text-xs">
              <span className="font-medium">
                {chosen.length} selected on this page
              </span>
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => setBulkOpen(true)}
              >
                Edit attribute
              </Button>
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => void bulk(true)}
              >
                Archive
              </Button>
              <button
                className="ml-auto underline"
                onClick={() => setSelected(new Set())}
              >
                Clear selection
              </button>
            </div>
          )}
          <ErrorText>{error}</ErrorText>
          {loading ? (
            <div className="rounded-xl border bg-card p-8">
              <Loading />
            </div>
          ) : !error && rows.length === 0 ? (
            <Empty
              title={
                config.q || Object.values(config.filters).some(Boolean)
                  ? "No matching records"
                  : `Your ${obj.plural.toLowerCase()} start here`
              }
            >
              {config.q || Object.values(config.filters).some(Boolean) ? (
                <button
                  className="underline"
                  onClick={() => change({ q: "", filters: {} })}
                >
                  Clear the search and filters
                </button>
              ) : (
                "Add a record or connect a data source to get started."
              )}
            </Empty>
          ) : !error && config.layout === "table" ? (
            <div className="overflow-x-auto rounded-xl border bg-card shadow-xs">
              <table className="w-full text-left text-xs">
                <thead className="border-b bg-muted/30 text-muted-foreground">
                  <tr>
                    {canManage && (
                      <th className="w-10 p-3">
                        <input
                          type="checkbox"
                          aria-label="Select this page"
                          checked={
                            rows.length > 0 && chosen.length === rows.length
                          }
                          onChange={(e) =>
                            setSelected(
                              new Set(
                                e.target.checked ? rows.map((r) => r.id) : [],
                              ),
                            )
                          }
                        />
                      </th>
                    )}
                    <th className="min-w-56 p-3 font-medium">{obj.name}</th>
                    {activeColumns.map((key) => (
                      <th key={key} className="min-w-40 p-3 font-medium">
                        {fields.find((f) => f.key === key)!.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {rows.map((r) => (
                    <tr key={r.id} className="hover:bg-muted/20">
                      {canManage && (
                        <td className="p-3">
                          <input
                            type="checkbox"
                            aria-label={`Select ${displayName(r)}`}
                            checked={selected.has(r.id)}
                            onChange={(e) =>
                              setSelected((prev) => {
                                const next = new Set(prev);
                                if (e.target.checked) next.add(r.id);
                                else next.delete(r.id);
                                return next;
                              })
                            }
                          />
                        </td>
                      )}
                      <td className="p-3">
                        <button
                          className="flex w-full items-center gap-2.5 rounded text-left focus-visible:outline-2 focus-visible:outline-ring"
                          aria-label={`Open ${displayName(r)}`}
                          onClick={() => openRecord(r.id)}
                        >
                          <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-muted/40 text-[10px] font-semibold">
                            {displayName(r).slice(0, 2).toUpperCase()}
                          </span>
                          <span className="min-w-0">
                            <span className="block font-medium">
                              {displayName(r)}
                            </span>
                            <span className="mt-0.5 block text-[11px] text-muted-foreground">
                              {identity(
                                r,
                                type === "account" ? "domain" : "email",
                              )}
                            </span>
                          </span>
                        </button>
                      </td>
                      {activeColumns.map((key) => (
                        <td key={key} className="max-w-72 px-2 py-2">
                          <InlineField
                            ws={ws}
                            record={r}
                            names={referenceNames}
                            f={fields.find((f) => f.key === key)!}
                            cat={cat}
                            canManage={canManage}
                            onSaved={refresh}
                          />
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : !error && group ? (
            <RecordBoard
              ws={ws}
              type={type}
              group={group}
              cat={cat}
              config={config}
              canManage={canManage}
              onOpen={openRecord}
              onChange={refresh}
              tick={tick}
            />
          ) : null}

          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>
              {total.toLocaleString()} {obj.plural.toLowerCase()}
              {total > 0 && config.layout === "table"
                ? ` · ${offset + 1}–${Math.min(offset + pageSize, total)}`
                : ""}
            </span>
            <div className="flex gap-2">
              {config.layout === "table" && (
                <>
                  <Button
                    variant="ghost"
                    disabled={loading || offset === 0}
                    onClick={() => setOffset((n) => Math.max(0, n - pageSize))}
                  >
                    <ChevronLeft className="size-3.5" />
                    Previous
                  </Button>
                  <Button
                    variant="ghost"
                    disabled={loading || !nextCursor}
                    onClick={() => { setCursors((prev) => [...prev.slice(0, offset / pageSize + 1), nextCursor ?? undefined]); setOffset((n) => n + pageSize); }}
                  >
                    Next
                    <ChevronRight className="size-3.5" />
                  </Button>
                </>
              )}
            </div>
          </div>
        </>
      )}
      {adding && obj && (
        <RecordDialog
          ws={ws}
          type={type}
          rec={null}
          cat={cat}
          names={{}}
          onClose={() => setAdding(false)}
          onSaved={(r) => {
            refresh();
            setOpen(`${type}:${r.id}`);
          }}
        />
      )}
      {preview && (
        <RecordPreview
          key={preview}
          ws={ws}
          id={preview}
          cat={cat}
          canManage={canManage}
          myId={myId}
          onClose={() => setOpen(section)}
          onChange={refresh}
          onOpen={openRecord}
          onFullPage={() => go("people", preview)}
        />
      )}
      {savingView && (
        <Dialog open onOpenChange={(v) => !v && !busy && setSavingView(false)}>
          <DialogContent>
            <form className="space-y-4" onSubmit={saveView}>
              <DialogHeader>
                <DialogTitle>Save this view</DialogTitle>
                <DialogDescription>
                  Keep the search, filters, columns, sort order and layout
                  together.
                </DialogDescription>
              </DialogHeader>
              <label className="block text-sm">
                View name
                <input
                  autoFocus
                  required
                  maxLength={100}
                  className={`${field} mt-1 w-full`}
                  placeholder="My active opportunities"
                  value={viewName}
                  onChange={(e) => setViewName(e.target.value)}
                />
              </label>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={shared}
                  onChange={(e) => setShared(e.target.checked)}
                />
                Share with the team
              </label>
              <ErrorText>{viewError}</ErrorText>
              <DialogFooter>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => setSavingView(false)}
                >
                  Cancel
                </Button>
                <Button disabled={busy || !viewName.trim()}>
                  {busy ? "Saving…" : "Save view"}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      )}
      {bulkOpen && (
        <Dialog open onOpenChange={(v) => !v && !busy && setBulkOpen(false)}>
          <DialogContent>
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                void bulk();
              }}
            >
              <DialogHeader>
                <DialogTitle>Edit {chosen.length} selected records</DialogTitle>
                <DialogDescription>
                  The chosen attribute will be updated on these records.
                </DialogDescription>
              </DialogHeader>
              <select
                aria-label="Attribute to update"
                required
                className={`${field} w-full`}
                value={bulkField}
                onChange={(e) => {
                  setBulkField(e.target.value);
                  setBulkValue(null);
                }}
              >
                <option value="">Choose an attribute</option>
                {fields
                  .filter(
                    (f) =>
                      !["computed", "source_owned"].includes(f.write_policy),
                  )
                  .map((f) => (
                    <option key={f.key} value={f.key}>
                      {f.label}
                    </option>
                  ))}
              </select>
              {fields.find((f) => f.key === bulkField) && (
                <FieldInput
                  ws={ws}
                  f={fields.find((f) => f.key === bulkField)!}
                  cat={cat}
                  value={bulkValue}
                  names={{}}
                  onChange={setBulkValue}
                />
              )}
              <DialogFooter>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => setBulkOpen(false)}
                >
                  Cancel
                </Button>
                <Button disabled={busy || !bulkField}>
                  {busy ? "Updating…" : "Update records"}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}
